package cli

import (
	"strings"
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

func TestImagePruneHelpDescribesDownloadCacheAndPreview(t *testing.T) {
	out, diagnostic, code := helpExecution(t, []string{"image", "prune", "--help"})
	if code != 0 || diagnostic != "" {
		t.Fatalf("prune help: %d %q %q", code, out, diagnostic)
	}
	for _, phrase := range []string{"compressed download blobs", "active pull", "preserves images and blobs", "eligible image IDs only"} {
		if !strings.Contains(out, phrase) {
			t.Fatalf("prune help missing %q: %s", phrase, out)
		}
	}
}
