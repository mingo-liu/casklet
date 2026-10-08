package cli

import (
	"testing"
)

func TestDiskManagementParsesFocusedOptions(t *testing.T) {
	for _, args := range [][]string{{"system", "df"}, {"system", "df", "--json"}, {"image", "prune"}, {"image", "prune", "--dry-run"}} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("%q: %v", args, err)
		}
	}
	for _, args := range [][]string{{"system"}, {"system", "prune"}, {"system", "df", "extra"}, {"image", "prune", "extra"}, {"image", "prune", "--force"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
