package container

import (
	"reflect"
	"testing"
)

func testFilters(t *testing.T, expressions ...string) []Filter {
	t.Helper()
	var filters []Filter
	for _, expr := range expressions {
		f, err := ParseFilter(expr)
		if err != nil {
			t.Fatal(err)
		}
		filters = append(filters, f)
	}
	return filters
}

func TestContainerFiltersIntersectLabelsStatesAndHealth(t *testing.T) {
	records := []Record{
		{ID: "a", State: StateRunning, Labels: map[string]string{"project": "demo", "role": "api", "empty": ""}, Health: &Health{Status: HealthHealthy}},
		{ID: "b", State: StateExited, Labels: map[string]string{"project": "demo", "role": "db"}, Health: &Health{Status: HealthHealthy}},
		{ID: "c", State: StateRunning, Labels: map[string]string{"project": "other"}},
		{ID: "d", State: StateRunning, Labels: map[string]string{"project": "a=b"}, Health: &Health{Status: HealthUnhealthy}},
	}
	for _, tt := range []struct {
		expr []string
		ids  []string
	}{
		{nil, []string{"a", "b", "c", "d"}},
		{[]string{"label=project"}, []string{"a", "b", "c", "d"}},
		{[]string{"label=project=demo"}, []string{"a", "b"}},
		{[]string{"label=project=demo", "label=role=api", "status=running", "health=healthy"}, []string{"a"}},
		{[]string{"label=empty="}, []string{"a"}},
		{[]string{"label=missing="}, nil},
		{[]string{"label=project=a=b"}, []string{"d"}},
		{[]string{"health=stopped"}, []string{"b"}},
		{[]string{"health=none"}, []string{"c"}},
		{[]string{"health=unhealthy"}, []string{"d"}},
		{[]string{"status=running", "status=exited"}, nil},
		{[]string{"label=project=demo", "label=project=other"}, nil},
	} {
		filtered := FilterRecords(records, testFilters(t, tt.expr...))
		var ids []string
		for _, record := range filtered {
			ids = append(ids, record.ID)
		}
		if !reflect.DeepEqual(ids, tt.ids) {
			t.Fatalf("%v -> %v, want %v", tt.expr, ids, tt.ids)
		}
	}
	if records[1].Health.Status != HealthHealthy {
		t.Fatal("filter mutated recorded health")
	}
	for _, state := range []string{StateCreated, StateStarting, StateRunning, StateStopping, StateExited, StateFailed} {
		if got := FilterRecords([]Record{{State: state}}, testFilters(t, "status="+state)); len(got) != 1 {
			t.Fatalf("state %s rejected", state)
		}
	}
}

func TestContainerFiltersRejectUnknownOrIncompleteSelectors(t *testing.T) {
	for _, expr := range []string{"", "label", "label=", "label=bad key", "status=stopped", "health=running", "health=", "name=redis", "Status=running", "status=running=other", "label=a=\n"} {
		if _, err := ParseFilter(expr); err == nil {
			t.Fatalf("accepted %q", expr)
		}
	}
}
