package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/project"
)

func parseProject(r Request, args []string) (Request, error) {
	if len(args) == 0 {
		return r, errors.New("project requires validate, up, down, ps, logs, exec, start, stop, or restart")
	}
	if args[0] == "--help" || args[0] == "-h" {
		if len(args) != 1 {
			return r, errors.New("project help takes no arguments")
		}
		return helpRequest([]string{"project"})
	}
	r.Action = "project-" + args[0]
	if args[0] == "exec" {
		return parseExecOperands(r, args[1:], 2)
	}
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch args[0] {
	case "validate":
		fs.StringVar(&r.ProjectConfig, "config", "", "project JSON file")
		fs.BoolVar(&r.JSON, "json", false, "print a validation summary")
	case "up":
		fs.BoolVar(&r.DryRun, "dry-run", false, "preview deployment changes")
		fs.BoolVar(&r.RemoveOrphans, "remove-orphans", false, "remove managed services absent from the configuration")
		fs.Func("recreate", "replace a logical service (repeatable)", func(value string) error { r.Recreate = append(r.Recreate, value); return nil })
		fs.BoolVar(&r.Wait, "wait", false, "wait for all services within the deployment deadline")
		fs.StringVar(&r.ProjectConfig, "config", "", "project JSON file")
		fs.DurationVar(&r.WaitTimeout, "timeout", 30*time.Second, "dependency health wait deadline")
		fs.BoolVar(&r.JSON, "json", false, "print JSON records")
	case "stop", "restart":
		fs.Func("timeout", "graceful shutdown override", func(value string) error {
			duration, err := time.ParseDuration(value)
			if err != nil {
				return err
			}
			r.StopTimeout = &duration
			return nil
		})
	case "start":
	case "down":
		fs.BoolVar(&r.RemoveVolumes, "volumes", false, "also remove project-owned data volumes")
	case "logs":
		fs.IntVar(&r.Tail, "tail", -1, "trailing lines per service")
		fs.BoolVar(&r.Follow, "follow", false, "follow selected executions")
		fs.BoolVar(&r.Follow, "f", false, "follow selected executions")
		fs.Func("service", "logical service name (repeatable)", func(value string) error {
			if err := project.ValidateName(value); err != nil {
				return err
			}
			r.Services = append(r.Services, value)
			return nil
		})
	case "ps":
		fs.BoolVar(&r.JSON, "json", false, "print JSON records")
	default:
		return r, fmt.Errorf("unknown project command %q", args[0])
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp("project "+args[0], fs, args[1:])
		}
		return r, err
	}
	if args[0] == "up" || args[0] == "validate" {
		if fs.NArg() != 0 || r.ProjectConfig == "" {
			return r, fmt.Errorf("project %s requires --config FILE and no operands", args[0])
		}
		if args[0] == "up" && (r.WaitTimeout <= 0 || r.WaitTimeout > 24*time.Hour) {
			return r, errors.New("project --timeout must be greater than zero and at most 24h")
		}
		plan, err := readProjectPlan(r.ProjectConfig)
		r.Project = plan
		if err == nil {
			_, err = project.Select(plan, r.Recreate)
		}
		return r, err
	}
	if args[0] == "logs" {
		explicit := false
		fs.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "tail" })
		if (explicit && r.Tail < 0) || r.Tail > 1000000 {
			return r, errors.New("--tail must be between 0 and 1000000")
		}
	}
	if args[0] == "start" || args[0] == "stop" || args[0] == "restart" {
		if fs.NArg() < 1 {
			return r, errors.New("project lifecycle command requires PROJECT [SERVICE ...]")
		}
		r.Reference, r.Services = fs.Arg(0), fs.Args()[1:]
		if r.StopTimeout != nil {
			if err := config.ValidateStopTimeout(*r.StopTimeout); err != nil {
				return r, err
			}
		}
		if len(r.Services) > 64 {
			return r, errors.New("project operations support at most 64 services")
		}
		seen := map[string]bool{}
		for _, key := range r.Services {
			if project.ValidateName(key) != nil || seen[key] {
				return r, errors.New("invalid or duplicate project service selection")
			}
			seen[key] = true
		}
		return r, project.ValidateName(r.Reference)
	}
	if fs.NArg() != 1 {
		return r, errors.New("project command requires exactly one project name; flags precede it")
	}
	r.Reference = fs.Arg(0)
	return r, project.ValidateName(r.Reference)
}

func readProjectPlan(filename string) (project.Plan, error) {
	var plan project.Plan
	filename, err := filepath.Abs(filename)
	if err != nil {
		return plan, err
	}
	data, err := readOptionFile(filename)
	if err != nil {
		return plan, err
	}
	doc, err := project.Decode(data)
	if err != nil {
		return plan, err
	}
	plan.Name = doc.Name
	networks := map[string]string{}
	for _, key := range doc.Networks {
		name, _ := project.ScopedName(doc.Name, key)
		networks[key] = name
		plan.Networks = append(plan.Networks, name)
	}
	volumes := map[string]string{}
	for _, key := range doc.Volumes {
		name, _ := project.ScopedName(doc.Name, key)
		volumes[key] = name
		plan.Volumes = append(plan.Volumes, name)
	}
	for logical, actual := range doc.ExternalVolumes {
		volumes[logical] = actual
		plan.ExternalVolumes = append(plan.ExternalVolumes, actual)
	}
	sort.Strings(plan.ExternalVolumes)
	order, _ := doc.Order()
	var published []config.PortMapping
	aliasesInUse := map[string]string{}
	healthPossible := map[string]bool{}
	for _, key := range order {
		service := doc.Services[key]
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(service.Run, &fields)
		for _, reserved := range []string{"name", "detach", "interactive", "tty", "rootless", "userns", "uid-map", "gid-map"} {
			if _, found := fields[reserved]; found {
				return plan, fmt.Errorf("service %q run field %q is managed by project", key, reserved)
			}
		}
		networkKey := "default"
		if raw, found := fields["network"]; found {
			if json.Unmarshal(raw, &networkKey) != nil {
				return plan, errors.New("service network must be a declared logical name or none")
			}
		}
		if networkKey == "none" {
			fields["network"] = json.RawMessage(`"none"`)
		} else {
			name, found := networks[networkKey]
			if !found {
				return plan, fmt.Errorf("service %q uses an undeclared network", key)
			}
			fields["network"], _ = json.Marshal(name)
			var aliases []string
			if raw, found := fields["network-alias"]; found && json.Unmarshal(raw, &aliases) != nil {
				return plan, errors.New("network-alias must be a string array")
			}
			if !containsProjectAlias(aliases, key) {
				aliases = append(aliases, key)
			}
			fields["network-alias"], _ = json.Marshal(aliases)
		}
		if raw, found := fields["mount"]; found {
			var mounts []string
			if json.Unmarshal(raw, &mounts) != nil {
				return plan, errors.New("service mount must be a string array")
			}
			for i, value := range mounts {
				mount, err := config.ParseMountFrom(value, filepath.Dir(filename))
				if err != nil {
					return plan, fmt.Errorf("service %q has an invalid mount", key)
				}
				if mount.Type == "volume" {
					name, found := volumes[mount.Source]
					if !found {
						return plan, fmt.Errorf("service %q uses an undeclared volume", key)
					}
					mounts[i] = "type=volume,source=" + name + ",target=" + mount.Target
					if mount.ReadOnly {
						mounts[i] += ",readonly"
					}
				}
			}
			fields["mount"], _ = json.Marshal(mounts)
		}
		raw, _ := json.Marshal(fields)
		options, command, err := configDataArguments(raw, filename, map[string]bool{})
		if err != nil {
			return plan, fmt.Errorf("invalid run fields for service %q", key)
		}
		name, _ := project.ScopedName(doc.Name, key)
		args := append([]string{"run", "--detach", "--name", name}, options...)
		if len(command) > 0 {
			args = append(append(args, "--"), command...)
		}
		args, err = expandFileArguments(args)
		if err != nil {
			return plan, fmt.Errorf("read service %q files: %w", key, err)
		}
		args = platformArguments(args)
		request, err := Parse(args)
		if err != nil {
			return plan, fmt.Errorf("invalid run configuration for service %q; see casklet run --help", key)
		}
		for label := range request.Config.Labels {
			if strings.HasPrefix(label, project.LabelPrefix) {
				return plan, errors.New("io.casklet.project.* labels are reserved")
			}
		}
		if len(request.Config.Labels) > 60 || service.Task && len(request.Config.Labels) > 59 {
			return plan, errors.New("project services support at most 60 user labels (59 for tasks)")
		}
		for _, port := range request.Config.Publish {
			for _, previous := range published {
				if config.PortsConflict(port, previous) {
					return plan, errors.New("project services publish conflicting host ports")
				}
			}
			published = append(published, port)
		}
		if service.Task {
			if request.Config.RestartMode() != "no" {
				return plan, fmt.Errorf("task %q requires restart=no", key)
			}
			h := request.Config.Healthcheck
			if h != nil && (len(h.Test) == 0 || h.Enabled()) {
				return plan, fmt.Errorf("task %q cannot enable health checks", key)
			}
			request.Config.Healthcheck = &config.HealthConfig{Test: []string{"NONE"}}
			if request.Config.Labels == nil {
				request.Config.Labels = map[string]string{}
			}
			request.Config.Labels[project.LabelTask] = "true"
			end := len(args)
			for i, arg := range args {
				if arg == "--" {
					end = i
					break
				}
			}
			args = append(append(append([]string(nil), args[:end]...), "--no-healthcheck", "--label", project.LabelTask+"=true"), args[end:]...)
		}
		// Keep absence distinct from explicit overrides of image-provided defaults.
		request.Config.Env = canonicalProjectEnvironment(request.Config.Env)
		healthPossible[key] = request.Config.Healthcheck.Enabled() || (request.Config.Image != "" && (request.Config.Healthcheck == nil || len(request.Config.Healthcheck.Test) == 0))
		for dep, condition := range service.DependsOn {
			if condition == "healthy" && !healthPossible[dep] {
				return plan, fmt.Errorf("service %q requires a healthcheck on dependency %q", key, dep)
			}
		}
		if config.IsNamedNetwork(request.Config.Network) {
			for _, alias := range append([]string{name}, request.Config.NetworkAliases...) {
				reservation := request.Config.Network + ":" + alias
				if owner, found := aliasesInUse[reservation]; found && owner != key {
					return plan, errors.New("project services reserve conflicting network aliases")
				}
				aliasesInUse[reservation] = key
			}
		}
		request.Config.NetworkAliases = slices.Clone(request.Config.NetworkAliases)
		sort.Strings(request.Config.NetworkAliases)
		fingerprint := projectFingerprint(request.Config, request.Entrypoint)
		labels := []string{"--label", project.LabelName + "=" + doc.Name, "--label", project.LabelService + "=" + key, "--label", project.LabelSpec + "=" + fingerprint}
		args = append(append([]string{args[0]}, labels...), args[1:]...)
		// Validate the final label budget and all generated options before starting a VM.
		validationArgs := append([]string{"run", "--label", project.LabelTransaction + "=" + strings.Repeat("0", 32)}, args[1:]...)
		if _, err := Parse(validationArgs); err != nil {
			return plan, fmt.Errorf("invalid generated configuration for service %q", key)
		}
		plan.Services = append(plan.Services, project.Service{Task: service.Task, Config: request.Config, Fields: project.FieldDigests(request.Config, request.Entrypoint), Key: key, Name: name, Args: args, Fingerprint: fingerprint, DependsOn: service.DependsOn,
			MatchLegacyFingerprint: func(saved string, aliases []string) bool {
				// The store appends the container name if it is not already an
				// explicit alias. It was absent from that legacy config hash.
				if !slices.Contains(request.Config.NetworkAliases, name) && len(aliases) > 0 && aliases[len(aliases)-1] == name {
					aliases = aliases[:len(aliases)-1]
				}
				sorted := slices.Clone(aliases)
				sort.Strings(sorted)
				if !slices.Equal(sorted, request.Config.NetworkAliases) {
					return false
				}
				legacy := request.Config
				legacy.NetworkAliases = aliases
				return projectFingerprint(legacy, request.Entrypoint) == saved
			}})
	}
	return plan, nil
}

func projectFingerprint(cfg config.Config, entrypoint *string) string {
	canonical, _ := json.Marshal(struct {
		Config     config.Config
		Entrypoint *string
	}{cfg, entrypoint})
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func containsProjectAlias(aliases []string, name string) bool {
	for _, alias := range aliases {
		if alias == name {
			return true
		}
	}
	return false
}

func canonicalProjectEnvironment(env []string) []string {
	values := map[string]string{}
	for _, assignment := range env {
		key, _, _ := strings.Cut(assignment, "=")
		values[key] = assignment
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}
