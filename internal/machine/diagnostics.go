package machine

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Diagnostics omits local paths, shares and SSH configuration from reports.
type Diagnostics struct {
	LimaVersion string        `json:"lima_version,omitempty"`
	VM          *DiagnosticVM `json:"vm,omitempty"`
}
type DiagnosticVM struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Arch   string `json:"arch"`
	CPUs   int    `json:"cpus"`
	Memory int64  `json:"memory_bytes"`
	Disk   int64  `json:"configured_disk_bytes"`
}

// Diagnose reads current Lima metadata without creating state or starting a VM.
func Diagnose(ctx context.Context) (Diagnostics, error) {
	var result Diagnostics
	lima, err := exec.LookPath("limactl")
	if err != nil {
		return result, errors.New("Lima is required; install it with brew install lima")
	}
	m := &Machine{lima: lima}
	data, err := m.command(ctx, "--version")
	if err != nil {
		return result, err
	}
	result.LimaVersion = strings.TrimSpace(string(data))
	instance, err := m.instance(ctx)
	if err != nil {
		return result, err
	}
	if instance != nil {
		result.VM = &DiagnosticVM{Name: instance.Name, Status: instance.Status, Arch: instance.Arch, CPUs: instance.CPUs, Memory: instance.Memory, Disk: instance.Disk}
	}
	return result, nil
}
