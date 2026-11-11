package machine

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (m *Machine) ensureProvisioned(ctx context.Context, instance Instance, preview bool) error {
	script := _guestProvisionScript
	if _, err := m.output(ctx, instance, "/usr/bin/sudo", "-n", "--", "/bin/sh", "-c", script, "casklet-provision", "--check"); err == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if preview {
		return errors.New("runtime machine initialization is incomplete; run casklet machine start before retrying project preview")
	}
	_, _ = fmt.Fprintln(m.stderr, "Preparing runtime machine dependencies...")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// The guest deadline also bounds package tools after SSH/client interruption.
	_, err := m.output(ctx, instance, "/usr/bin/sudo", "-n", "--", "/usr/bin/timeout", "--kill-after=10s", "9m", "/bin/sh", "-c", script, "casklet-provision")
	if err != nil {
		return fmt.Errorf("runtime machine initialization failed: %w; check network access to Ubuntu package repositories, then retry casklet machine start", err)
	}
	return nil
}

func initialProvisionScript() string {
	return "#!/bin/sh\nexec /usr/bin/timeout --kill-after=10s 9m /bin/sh -c " + quote(_guestProvisionScript) + " casklet-provision '{{.User}}' '{{.UID}}'\n"
}
