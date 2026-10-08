//go:build darwin

package macos

import (
	"encoding/json"
	"github.com/mingo-liu/casklet/internal/storage"
	"testing"
)

func TestDiskUsageThroughMacClient(t *testing.T) {
	var r storage.Report
	if err := json.Unmarshal([]byte(success(t, "system", "df", "--json")), &r); err != nil {
		t.Fatal(err)
	}
	if r.Filesystem.TotalBytes == 0 || len(r.Categories) != 5 {
		t.Fatalf("report %+v", r)
	}
	// User caches remain intact: actual pruning is verified in the isolated Linux VM.
	success(t, "image", "prune", "--dry-run")
}
