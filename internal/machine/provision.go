package machine

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func initialProvisionScript() string {
	return "#!/bin/sh\nexec /usr/bin/timeout --kill-after=10s 9m /bin/sh -c " + quote(guestProvisionScript) + " casklet-provision '{{.User}}' '{{.UID}}'\n"
}

func (m *Machine) ensureProvisioned(ctx context.Context, instance Instance, preview bool) error {
	script := guestProvisionScript
	if _, err := m.output(ctx, instance, "/usr/bin/sudo", "-n", "--", "/bin/sh", "-c", script, "casklet-provision", "--check"); err == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if preview {
		return errors.New("runtime machine initialization is incomplete; run casklet machine start before retrying project preview")
	}
	fmt.Fprintln(m.stderr, "Preparing runtime machine dependencies...")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// The guest deadline also bounds package tools after SSH/client interruption.
	_, err := m.output(ctx, instance, "/usr/bin/sudo", "-n", "--", "/usr/bin/timeout", "--kill-after=10s", "9m", "/bin/sh", "-c", script, "casklet-provision")
	if err != nil {
		return fmt.Errorf("runtime machine initialization failed: %w; check network access to Ubuntu package repositories, then retry casklet machine start", err)
	}
	return nil
}
