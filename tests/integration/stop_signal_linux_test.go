//go:build linux

package integration

import (
	"context"
	"github.com/mingo-liu/casklet/internal/image"
	"strings"
	"testing"
	"time"
)

func TestManagedStopSignalOverrideAndRestart(t *testing.T) {
	for _, signal := range []string{"SIGUSR1", "10"} {
		t.Run(signal, func(t *testing.T) {
			id := startBackground(t, backgroundName(t), []string{"--stop-signal", signal, "--stop-timeout", "2s"}, "/bin/sh", "-c", "trap 'echo custom-stop; exit 23' USR1; echo signal-ready; while :; do sleep .05; done")
			lifecycleReady(t, id, "signal-ready")
			backgroundSuccess(t, "stop", id)
			lifecycleWait(t, id, 23)
			backgroundSuccess(t, "restart", id)
			deadline := time.Now().Add(5 * time.Second)
			for strings.Count(backgroundSuccess(t, "logs", id), "signal-ready") < 2 {
				if time.Now().After(deadline) {
					t.Fatal("restarted command not ready")
				}
				time.Sleep(25 * time.Millisecond)
			}
			backgroundSuccess(t, "stop", id)
			lifecycleWait(t, id, 23)
			if out := backgroundSuccess(t, "inspect", id); !strings.Contains(out, `"stop_signal": "`+signal+`"`) {
				t.Fatal(out)
			}
		})
	}
}
func TestOCIStopSignalIsRetainedAfterRegistryDisappears(t *testing.T) {
	ref, closeRegistry := ociRegistryFixture(t)
	id := backgroundSuccess(t, "image", "pull", ref)
	id = strings.TrimSpace(id)
	store, err := image.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Remove(context.Background(), id, func(context.Context, string) (bool, error) { return false, nil })
	})
	closeRegistry()
	for _, override := range []bool{false, true} {
		options := []string{"--image", id, "--entrypoint", "", "--stop-timeout", "2s"}
		expected := 27
		if override {
			options = append(options, "--stop-signal", "SIGUSR2")
			expected = 28
		}
		name := backgroundName(t)
		args := append([]string{"run", "-d", "--name", name}, options...)
		args = append(args, "--", "/bin/sh", "-c", "trap 'exit 27' USR1; trap 'exit 28' USR2; echo signal-ready; while :; do sleep .05; done")
		container := strings.TrimSpace(backgroundSuccess(t, args...))
		lifecycleReady(t, container, "signal-ready")
		backgroundSuccess(t, "stop", container)
		lifecycleWait(t, container, expected)
	}
}
