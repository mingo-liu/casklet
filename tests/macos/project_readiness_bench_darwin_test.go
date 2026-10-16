//go:build darwin

package macos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

// Opt-in benchmark includes the real client/SSH transport. Setup and teardown
// are outside timing; workloads and repetitions are explicitly bounded.
func BenchmarkProjectReadinessSampling(b *testing.B) {
	if os.Getenv("CASKLET_MACOS_BENCHMARK") != "1" {
		b.Skip("set CASKLET_MACOS_BENCHMARK=1 with CASKLET_MACOS_BINARY")
	}
	binary := os.Getenv("CASKLET_MACOS_BINARY")
	if binary == "" {
		b.Fatal("CASKLET_MACOS_BINARY is required")
	}
	call := func(args ...string) []byte {
		b.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		var diagnostic strings.Builder
		cmd.Stderr = &diagnostic
		data, err := cmd.Output()
		if err != nil {
			b.Fatalf("%v: %v %s", args, err, diagnostic.String())
		}
		return data
	}
	for _, count := range []int{16, 64} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			name := fmt.Sprintf("sample-%x", time.Now().UnixNano())
			services := map[string]any{}
			for i := 0; i < count; i++ {
				services[fmt.Sprintf("s%02d", i)] = map[string]any{"run": map[string]any{"network": "none", "command": []string{"/bin/sleep", "300"}}}
			}
			data, _ := json.Marshal(map[string]any{"name": name, "networks": []string{}, "services": services})
			file := filepath.Join(b.TempDir(), "project.json")
			if err := os.WriteFile(file, data, 0600); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { call("project", "down", name) })
			call("project", "up", "--config", file)
			var records []container.Record
			if err := json.Unmarshal(call("project", "ps", "--json", name), &records); err != nil || len(records) != count {
				b.Fatalf("records: %v %v", records, err)
			}
			for _, strategy := range []string{"LegacyInspect", "BatchStatus"} {
				b.Run(strategy, func(b *testing.B) {
					b.ResetTimer()
					if b.N > 10 {
						b.Fatal("use -benchtime=1x through 10x to bound live workload sampling")
					}
					for i := 0; i < b.N; i++ {
						if strategy == "LegacyInspect" {
							for _, record := range records {
								var value container.Inspection
								if err := json.Unmarshal(call("inspect", record.ID), &value); err != nil || value.ID != record.ID || value.Generation != record.Generation || value.State != container.StateRunning {
									b.Fatal("invalid legacy sample", err)
								}
							}
						} else {
							var values []project.ServiceStatus
							if err := json.Unmarshal(call("__project-status", name), &values); err != nil || len(values) != count {
								b.Fatal("invalid batch sample", err)
							}
							identities := map[string]project.ServiceStatus{}
							for _, value := range values {
								identities[value.ID] = value
							}
							for _, record := range records {
								value := identities[record.ID]
								if value.Generation != record.Generation || value.State != container.StateRunning {
									b.Fatal("changed batch identity")
								}
							}
						}
					}
					b.ReportMetric(float64(count), "services")
					if strategy == "LegacyInspect" {
						b.ReportMetric(float64(count), "calls/sample")
					} else {
						b.ReportMetric(1, "calls/sample")
					}
				})
			}
		})
	}
}
