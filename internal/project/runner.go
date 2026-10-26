package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

// Call executes the existing CLI operations, preserving platform authentication,
// path translation, host-port checks and guest lifecycle fencing.
type Call func(context.Context, ...string) ([]byte, error)

type Runner struct {
	Backend Backend
	Call    Call
	Save    func(State) error
	State   State
	Preview []Change
}

func (runner *Runner) Records(ctx context.Context, names ...string) ([]container.Record, error) {
	name := ""
	if len(names) > 0 {
		name = names[0]
	}
	return runner.Backend.Records(ctx, name)
}

func Managed(records []container.Record, name string) []container.Record {
	result := []container.Record{}
	for _, record := range records {
		if record.Labels[LabelName] == name && record.Labels[LabelService] != "" && record.Labels[LabelSpec] != "" {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (runner *Runner) resources(ctx context.Context, kind string) (map[string]Resource, error) {
	values, err := runner.Backend.Resources(ctx, kind)
	if err != nil {
		return nil, err
	}
	result := map[string]Resource{}
	for _, value := range values {
		value.Kind = kind
		if value.Name == "" || value.CreatedAt.IsZero() {
			return nil, errors.New("invalid resource identity")
		}
		result[value.Name] = value
	}
	return result, nil
}

func sameResource(a, b Resource) bool {
	return a.Kind == b.Kind && a.Name == b.Name && a.CreatedAt.Equal(b.CreatedAt)
}

type Change struct {
	Reason             string   `json:"reason"`
	ChangedFields      []string `json:"changed_fields,omitempty"`
	RequiresRecreate   bool     `json:"requires_recreate,omitempty"`
	DetailsUnavailable bool     `json:"details_unavailable,omitempty"`
	Kind               string   `json:"kind"`
	Name               string   `json:"name"`
	Action             string   `json:"action"`
	ID                 string   `json:"id,omitempty"`
}

type UpOptions struct {
	Recreate      []string
	DryRun        bool
	RemoveOrphans bool

	Timeout time.Duration
	Wait    bool
	// Deadline shares the CLI's budget with local project lock acquisition.
	Deadline time.Time
}

func (runner *Runner) Up(ctx context.Context, plan Plan, options UpOptions) (records []container.Record, err error) {
	runner.Preview = nil
	selected, selectionErr := Select(plan, options.Recreate)
	if selectionErr != nil {
		return nil, selectionErr
	}
	parent := ctx
	failureCaptured, deadlineFailure := false, false
	if options.Wait {
		var cancel context.CancelFunc
		if options.Deadline.IsZero() {
			ctx, cancel = context.WithTimeout(ctx, options.Timeout)
		} else {
			ctx, cancel = context.WithDeadline(ctx, options.Deadline)
		}
		defer cancel()
		defer func() {
			if !failureCaptured {
				deadlineFailure = parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded)
			}
			if err != nil && deadlineFailure {
				err = errors.Join(ErrWaitTimeout, err)
			}
		}()
	}
	all, err := runner.Records(ctx)
	if err != nil {
		return nil, err
	}
	existing := map[string]container.Record{}
	matching := map[string]bool{}
	desired := map[string]bool{}
	for _, service := range plan.Services {
		desired[service.Key] = true
		for _, record := range all {
			if record.Name == service.Name {
				if record.Labels[LabelName] != plan.Name || record.Labels[LabelService] != service.Key || record.Labels[LabelSpec] == "" {
					return nil, fmt.Errorf("service %q already exists with different configuration or ownership; use --recreate for configuration changes or project down", service.Key)
				}
				matches := record.Labels[LabelSpec] == service.Fingerprint
				if !matches && service.MatchLegacyFingerprint != nil {
					data, err := runner.Call(ctx, "inspect", record.ID)
					if err != nil {
						return nil, err
					}
					var inspection container.Inspection
					if json.Unmarshal(data, &inspection) != nil || inspection.ID != record.ID || inspection.Generation != record.Generation {
						return nil, fmt.Errorf("service %q changed during configuration preflight", service.Key)
					}
					matches = service.MatchLegacyFingerprint(record.Labels[LabelSpec], inspection.Config.NetworkAliases)
				}
				if !matches && !selected[service.Key] && !options.DryRun {
					return nil, fmt.Errorf("service %q already exists with different configuration or ownership; use --recreate for configuration changes or project down", service.Key)
				}
				matching[service.Key] = matches
				existing[service.Key] = record
			}
		}
	}
	orphans := []container.Record{}
	for _, record := range Managed(all, plan.Name) {
		if expected, found := existing[record.Labels[LabelService]]; desired[record.Labels[LabelService]] && (!found || expected.ID != record.ID) {
			return nil, errors.New("project has duplicate or renamed service records; use project down before recreating")
		}
		if !desired[record.Labels[LabelService]] {
			if !options.RemoveOrphans {
				return nil, errors.New("project has services absent from this config; use --remove-orphans or project down")
			}
			orphans = append(orphans, record)
		}
	}
	removing := maps.Clone(existing)
	removalSelection := maps.Clone(selected)
	for _, record := range orphans {
		key := record.Labels[LabelService]
		name, err := ScopedName(plan.Name, key)
		if err != nil || ValidateName(key) != nil || record.Name != name || removalSelection[key] {
			return nil, errors.New("project has duplicate or renamed orphan service records; use project down after inspection")
		}
		removing[key], removalSelection[key] = record, true
	}
	removalCount := 0
	for key := range removing {
		if removalSelection[key] {
			removalCount++
		}
	}
	if removalCount > ReservationPageSize {
		return nil, errors.New("project removal selection exceeds 64 services")
	}
	// The old dependency graph is not retained. Remove orphans in reverse
	// creation order, with a stable full-ID tie break.
	sort.Slice(orphans, func(i, j int) bool {
		if orphans[i].CreatedAt.Equal(orphans[j].CreatedAt) {
			return orphans[i].ID > orphans[j].ID
		}
		return orphans[i].CreatedAt.After(orphans[j].CreatedAt)
	})
	inventories := map[string]map[string]Resource{}
	for _, kind := range []string{"network", "volume"} {
		inventory, e := runner.resources(ctx, kind)
		if e != nil {
			return nil, e
		}
		inventories[kind] = inventory
	}
	for _, name := range plan.ExternalVolumes {
		if _, found := inventories["volume"][name]; !found {
			return nil, fmt.Errorf("external volume %q does not exist; create or restore it before project up", name)
		}
	}
	var needed []Resource
	for _, entry := range []struct {
		kind  string
		names []string
	}{{"network", plan.Networks}, {"volume", plan.Volumes}} {
		for _, name := range entry.names {
			key := entry.kind + ":" + name
			if resource, found := inventories[entry.kind][name]; found {
				owned, ok := runner.State.Resources[key]
				if !ok || !sameResource(owned, resource) {
					return nil, fmt.Errorf("%s %q exists without matching project ownership", entry.kind, name)
				}
			} else {
				needed = append(needed, Resource{Kind: entry.kind, Name: name})
			}
		}
	}
	newKeys := 0
	for _, resource := range needed {
		if _, found := runner.State.Resources[resource.Kind+":"+resource.Name]; !found {
			newKeys++
		}
	}
	releasedKeys := 0
	for _, name := range plan.ExternalVolumes {
		if _, found := runner.State.Resources["volume:"+name]; found {
			releasedKeys++
		}
	}
	if len(runner.State.Resources)-releasedKeys+newKeys > 64 {
		return nil, errors.New("project resource history is full; use project down before recreating")
	}
	if len(removalSelection) > 0 || options.DryRun {
		planned := maps.Clone(removalSelection)
		if options.DryRun {
			for key := range existing {
				if !matching[key] {
					planned[key] = true
				}
			}
		}
		if err := runner.checkConflicts(ctx, plan, all, removing, planned); err != nil {
			return nil, err
		}
	}
	for _, resource := range needed {
		runner.Preview = append(runner.Preview, Change{Kind: resource.Kind, Name: resource.Name, Action: "create", Reason: "resource_missing"})
	}
	for _, record := range orphans {
		runner.Preview = append(runner.Preview, Change{Kind: "service", Name: record.Labels[LabelService], ID: record.ID, Action: "remove", Reason: "service_removed"})
	}
	for _, service := range plan.Services {
		record, found := existing[service.Key]
		runner.Preview = append(runner.Preview, runner.serviceChange(service, record, found, matching[service.Key], selected[service.Key]))
	}
	if options.DryRun {
		return nil, ctx.Err()
	}
	// Check every source before removing any old execution. Pin resolved images
	// for this invocation while retaining the desired-reference fingerprint.
	prepared := map[string][]string{}
	if len(removalSelection) > 0 {
		oldPorts, err := runner.selectedPorts(ctx, removing, removalSelection)
		if err != nil {
			return nil, err
		}
		for _, service := range plan.Services {
			if record, found := existing[service.Key]; found && !selected[service.Key] {
				if record.State != container.StateRunning {
					args, err := runner.retainedPreflightArguments(ctx, record, plan.Name, service.Key)
					if err != nil {
						return nil, err
					}
					if _, err := runner.Call(ctx, args...); err != nil {
						return nil, fmt.Errorf("service %q retained-source preflight: %w", service.Key, err)
					}
				}
				continue
			}
			args := preflightArguments(service.Args, oldPorts)
			data, err := runner.Call(ctx, args...)
			if err != nil {
				return nil, fmt.Errorf("service %q preflight: %w", service.Key, err)
			}
			args = append([]string(nil), service.Args...)
			if imageID := strings.TrimSpace(string(data)); imageID != "" {
				if !strings.HasPrefix(imageID, "sha256:") || len(imageID) != 71 {
					return nil, errors.New("invalid prepared project image identity")
				}
				if _, err := hex.DecodeString(imageID[7:]); err != nil {
					return nil, errors.New("invalid prepared project image identity")
				}
				for i := 1; i < len(args) && args[i] != "--"; i++ {
					if args[i] == "--image" {
						args[i+1] = imageID
						break
					}
					if strings.HasPrefix(args[i], "--image=") {
						args[i] = "--image=" + imageID
						break
					}
				}
			}
			prepared[service.Key] = args
		}
	}
	// An explicit external declaration relinquishes any old ownership receipt,
	// including one left behind after restoring the same name. Persist that
	// release before mutations so rollback and config-free down cannot delete it.
	if releasedKeys > 0 {
		releasedState := runner.State
		releasedState.Resources = maps.Clone(runner.State.Resources)
		for _, name := range plan.ExternalVolumes {
			delete(releasedState.Resources, "volume:"+name)
		}
		if err := runner.Save(releasedState); err != nil {
			return nil, err
		}
		runner.State = releasedState
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	var created []Resource
	defer func() {
		if err != nil {
			failureCaptured = true
			deadlineFailure = parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded)
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			err = errors.Join(err, runner.rollback(cleanupCtx, plan.Name, token, created))
		}
	}()
	for _, resource := range needed {
		receipt, e := runner.Backend.CreateResource(ctx, resource.Kind, resource.Name)
		if e != nil {
			return nil, e
		}
		if receipt.Kind != resource.Kind || receipt.Name != resource.Name || receipt.CreatedAt.IsZero() {
			return nil, errors.New("invalid project creation receipt; preserving unverified resource")
		}
		created = append(created, receipt)
		runner.State.Resources[resource.Kind+":"+resource.Name] = receipt
		if err = runner.Save(runner.State); err != nil {
			return nil, err
		}
	}

	for _, record := range orphans {
		if _, err = runner.Call(ctx, "__project-replace", record.ID, fmt.Sprint(record.Generation), plan.Name, record.Labels[LabelService], record.Labels[LabelSpec]); err != nil {
			return nil, err
		}
		if err = runner.forgetService(record); err != nil {
			return nil, err
		}
	}
	// Remove only pinned selected records, under the guest lifecycle lock. All
	// removals precede creation so selected services can exchange ports/aliases.
	for i := len(plan.Services) - 1; i >= 0; i-- {
		service := plan.Services[i]
		if record, found := existing[service.Key]; found && selected[service.Key] {
			if _, err = runner.Call(ctx, "__project-replace", record.ID, fmt.Sprint(record.Generation), plan.Name, service.Key, record.Labels[LabelSpec]); err != nil {
				return nil, err
			}
			delete(existing, service.Key)
		}
	}
	// Lima releases the selected old forwarding sockets asynchronously. The
	// second preflight includes their bindings and uses the bounded host wait.
	for _, service := range plan.Services {
		if args, found := prepared[service.Key]; found {
			if _, err = runner.Call(ctx, append([]string{"__project-preflight"}, args[1:]...)...); err != nil {
				return nil, err
			}
		}
	}
	ids := map[string]string{}
	executions := map[string]serviceExecution{}
	for _, service := range plan.Services {
		dependencies := make([]string, 0, len(service.DependsOn))
		for dep := range service.DependsOn {
			dependencies = append(dependencies, dep)
		}
		sort.Strings(dependencies)
		for _, dep := range dependencies {
			if service.DependsOn[dep] == "healthy" {
				if _, err = runner.Call(ctx, "wait", "--healthy", "--timeout", options.Timeout.String(), ids[dep]); err != nil {
					return nil, fmt.Errorf("service %q dependency %q did not become healthy: %w", service.Key, dep, err)
				}
			} else {
				data, e := runner.Call(ctx, "inspect", ids[dep])
				if e != nil {
					return nil, e
				}
				var inspection container.Inspection
				if json.Unmarshal(data, &inspection) != nil || inspection.ID != ids[dep] || inspection.State != container.StateRunning {
					return nil, fmt.Errorf("service %q dependency %q is not running", service.Key, dep)
				}
			}
		}
		if record, ok := existing[service.Key]; ok {
			ids[service.Key] = record.ID
			if record.State != container.StateRunning {
				if _, err = runner.Call(ctx, "start", record.ID); err != nil {
					return nil, err
				}
			}
		} else {
			serviceArgs := service.Args
			if args, found := prepared[service.Key]; found {
				serviceArgs = args
			}
			args := append([]string{"run", "--label", LabelTransaction + "=" + token}, serviceArgs[1:]...)
			data, e := runner.Call(ctx, args...)
			if e != nil {
				return nil, e
			}
			id := strings.TrimSpace(string(data))
			if len(id) != 32 {
				return nil, errors.New("invalid new container identity")
			}
			if _, e := hex.DecodeString(id); e != nil {
				return nil, errors.New("invalid new container identity")
			}
			ids[service.Key] = id
		}
		if options.Wait {
			inspection, e := runner.inspectService(ctx, service.Key, ids[service.Key])
			if e != nil {
				return nil, e
			}
			if record, reused := existing[service.Key]; reused && record.State == container.StateRunning && record.Generation != inspection.Generation {
				return nil, fmt.Errorf("service %q execution changed during startup", service.Key)
			}
			health := inspection.Config.Healthcheck
			executions[service.Key] = serviceExecution{ID: inspection.ID, Generation: inspection.Generation,
				Healthcheck: health != nil && len(health.Test) > 0 && health.Test[0] != "NONE"}
		}
	}
	if options.Wait {
		if err = runner.waitServices(ctx, plan, executions); err != nil {
			return nil, err
		}
	}
	all, err = runner.Records(ctx, plan.Name)
	if err != nil {
		return nil, err
	}
	if options.Wait && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	records = Managed(all, plan.Name)
	for _, record := range records {
		if ids[record.Labels[LabelService]] != record.ID {
			return nil, errors.New("project service identity changed during startup")
		}
		if previous, reused := existing[record.Labels[LabelService]]; reused && previous.State == container.StateRunning && record.Generation != previous.Generation {
			return nil, errors.New("project service execution changed during startup")
		}
		if options.Wait {
			execution := executions[record.Labels[LabelService]]
			if record.Generation != execution.Generation || !execution.ready(record.State, record.Health) {
				return nil, errors.New("project service readiness changed during startup")
			}
		}
	}
	if len(records) != len(plan.Services) {
		return nil, errors.New("project services changed during startup")
	}
	if err = runner.rememberServices(plan, records); err != nil {
		return nil, err
	}
	return records, nil
}

func (runner *Runner) rollback(ctx context.Context, name, token string, created []Resource) error {
	records, err := runner.Records(ctx, name)
	var failures []error
	if err != nil {
		failures = append(failures, err)
	} else {
		for i := len(records) - 1; i >= 0; i-- {
			record := records[i]
			if record.Labels[LabelName] != name || record.Labels[LabelTransaction] != token {
				continue
			}
			if _, err := runner.Call(ctx, "stop", "--timeout", "0s", record.ID); err != nil {
				failures = append(failures, err)
				continue
			}
			if _, err := runner.Call(ctx, "rm", record.ID); err != nil {
				failures = append(failures, err)
			}
		}
	}
	for i := len(created) - 1; i >= 0; i-- {
		if err := runner.removeResource(ctx, created[i]); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (runner *Runner) removeResource(ctx context.Context, resource Resource) error {
	values, err := runner.resources(ctx, resource.Kind)
	if err != nil {
		return err
	}
	current, found := values[resource.Name]
	if found {
		if !sameResource(current, resource) {
			return fmt.Errorf("%s %q was replaced; preserving it", resource.Kind, resource.Name)
		}
		if err := runner.Backend.RemoveResource(ctx, resource); err != nil {
			return err
		}
	}
	delete(runner.State.Resources, resource.Kind+":"+resource.Name)
	return runner.Save(runner.State)
}

func (runner *Runner) Down(ctx context.Context, name string, removeVolumes bool) error {
	records, err := runner.Records(ctx, name)
	if err != nil {
		return err
	}
	var failures []error
	for _, record := range Managed(records, name) {
		if _, err := runner.Call(ctx, "stop", record.ID); err != nil {
			failures = append(failures, err)
			continue
		}
		if _, err := runner.Call(ctx, "rm", record.ID); err != nil {
			failures = append(failures, err)
		} else if err := runner.forgetService(record); err != nil {
			failures = append(failures, err)
		}
	}
	keys := make([]string, 0, len(runner.State.Resources))
	for key := range runner.State.Resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		resource := runner.State.Resources[key]
		if resource.Kind == "volume" && !removeVolumes {
			continue
		}
		if err := runner.removeResource(ctx, resource); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
