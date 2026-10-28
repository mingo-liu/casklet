package cli

import (
	"testing"
)

func TestEventsParsingAndFilters(t *testing.T) {
	r, err := Parse([]string{"events", "--follow", "--json", "--after", "42", "--filter", "project=demo", "--filter", "event=exit"})
	if err != nil || !r.Events.Follow || !r.JSON || r.Events.After == nil || *r.Events.After != 42 || len(r.Events.Filters) != 2 {
		t.Fatalf("parse %+v %v", r, err)
	}
	for _, args := range [][]string{{"events", "operand"}, {"events", "--after", "-1"}, {"events", "--since", "yesterday"}, {"events", "--filter", "event=unknown"}, {"events", "--filter", "label=secret"}, {"events", "--filter", "event"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
