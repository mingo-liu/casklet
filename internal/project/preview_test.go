package project

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestPreviewReportsChangedFieldsWithoutValuesOrMutation(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	for i := range plan.Services {
		plan.Services[i].Fields = FieldDigests(plan.Services[i].Config, nil)
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	mutations, saves := len(fake.mutations), fake.saves
	before, _ := json.Marshal(runner.State)
	plan.Services[1].Fingerprint = "updated"
	plan.Services[1].Config.Memory = 256 << 20
	plan.Services[1].Config.Env = []string{"TOKEN=preview-secret"}
	plan.Services[1].Fields = FieldDigests(plan.Services[1].Config, nil)
	if _, err := runner.Up(context.Background(), plan, UpOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	change := runner.Preview[len(runner.Preview)-1]
	if change.Action != "recreate" || change.Reason != "configuration_changed" || !change.RequiresRecreate || change.DetailsUnavailable || !reflect.DeepEqual(change.ChangedFields, []string{"env", "memory"}) {
		t.Fatalf("preview=%+v", change)
	}
	after, _ := json.Marshal(runner.State)
	output, _ := json.Marshal(runner.Preview)
	if string(before) != string(after) || saves != fake.saves || mutations != len(fake.mutations) || strings.Contains(string(output)+string(after), "preview-secret") {
		t.Fatalf("preview wrote state or exposed values: %s %s", output, after)
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{}); err == nil {
		t.Fatal("actual deployment accepted drift without --recreate")
	}
}

func TestPreviewDistinguishesExplicitRecreationAndUnavailableHistory(t *testing.T) {
	for _, scenario := range []string{"explicit", "legacy", "wrong-id", "wrong-spec"} {
		t.Run(scenario, func(t *testing.T) {
			fake, runner, plan := deployedProject(t)
			selected := []string{"api"}
			if scenario != "explicit" {
				plan.Services[1].Fingerprint = "changed"
				plan.Services[1].Fields = FieldDigests(config.Config{Memory: 256 << 20}, nil)
			}
			if scenario == "wrong-id" || scenario == "wrong-spec" {
				runner.State.Services = map[string]ServiceReceipt{"api": {ID: fake.records[1].ID, Spec: fake.records[1].Labels[LabelSpec], Fields: FieldDigests(config.Config{}, nil)}}
				if scenario == "wrong-id" {
					receipt := runner.State.Services["api"]
					receipt.ID = "other"
					runner.State.Services["api"] = receipt
				}
				if scenario == "wrong-spec" {
					receipt := runner.State.Services["api"]
					receipt.Spec = "other"
					runner.State.Services["api"] = receipt
				}
			}
			if _, err := runner.Up(context.Background(), plan, UpOptions{DryRun: true, Recreate: selected}); err != nil {
				t.Fatal(err)
			}
			change := runner.Preview[len(runner.Preview)-1]
			if scenario == "explicit" {
				if change.Reason != "explicit_recreate" || change.DetailsUnavailable {
					t.Fatalf("explicit=%+v", change)
				}
			} else if change.Reason != "configuration_changed" || !change.DetailsUnavailable || len(change.ChangedFields) != 0 {
				t.Fatalf("unverified history=%+v", change)
			}
		})
	}
}

func TestFieldDigestsTrackRedactedEntrypointAndRemovedOverrides(t *testing.T) {
	entrypoint := "/secret-entrypoint"
	first := FieldDigests(config.Config{Env: []string{"TOKEN=private-value"}, Memory: 128 << 20}, &entrypoint)
	second := FieldDigests(config.Config{Memory: 128 << 20}, nil)
	if first["env"] == "" || second["env"] != "" || first["entrypoint"] == second["entrypoint"] || first["memory"] != second["memory"] {
		t.Fatalf("incorrect field comparisons: %v %v", first, second)
	}
	data, _ := json.Marshal(first)
	if strings.Contains(string(data), "private-value") || strings.Contains(string(data), "secret-entrypoint") {
		t.Fatalf("baseline contains raw values: %s", data)
	}
}

func TestServiceHistoryIsNotWrittenForFailedDeploymentAndIsRemovedOnDown(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	for i := range plan.Services {
		plan.Services[i].Fields = FieldDigests(plan.Services[i].Config, nil)
	}
	fake.failRun = "cp-demo-api"
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err == nil || len(runner.State.Services) != 0 {
		t.Fatalf("failed up saved a baseline: %+v %v", runner.State.Services, err)
	}
	fake.failRun = ""
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if len(runner.State.Services) != 2 {
		t.Fatalf("missing successful baseline: %+v", runner.State.Services)
	}
	if err := runner.Down(context.Background(), plan.Name, false); err != nil || len(runner.State.Services) != 0 {
		t.Fatalf("down retained service comparisons: %+v %v", runner.State.Services, err)
	}
}

func TestPreviewTreatsVerifiedLegacyAliasFingerprintAsUnchanged(t *testing.T) {
	_, runner, plan := deployedProject(t)
	plan.Services[1].Fingerprint = "canonical"
	plan.Services[1].MatchLegacyFingerprint = func(string, []string) bool { return true }
	if _, err := runner.Up(context.Background(), plan, UpOptions{DryRun: true, Recreate: []string{"api"}}); err != nil {
		t.Fatal(err)
	}
	change := runner.Preview[len(runner.Preview)-1]
	if change.Reason != "explicit_recreate" || len(change.ChangedFields) != 0 {
		t.Fatalf("legacy alias normalization became drift: %+v", change)
	}
}

func TestComparisonPublicationFailureRollsBackOnlyNewServices(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	for i := range plan.Services {
		plan.Services[i].Fields = FieldDigests(plan.Services[i].Config, nil)
	}
	initial := plan
	initial.Services = initial.Services[:1]
	if _, err := runner.Up(context.Background(), initial, UpOptions{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	originalID := fake.records[0].ID
	before, _ := json.Marshal(runner.State.Services)
	save := runner.Save
	runner.Save = func(state State) error {
		if len(state.Services) == 2 {
			return errors.New("comparison storage unavailable")
		}
		return save(state)
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err == nil {
		t.Fatal("expected comparison publication failure")
	}
	after, _ := json.Marshal(runner.State.Services)
	if len(fake.records) != 1 || fake.records[0].ID != originalID || string(before) != string(after) {
		t.Fatalf("publication failure changed existing services/history: %+v %s", fake.records, after)
	}
}
