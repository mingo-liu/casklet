package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func sharedDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("shared directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("shared directory: %w", err)
	}
	if !info.IsDir() || absolute == "/" {
		return "", fmt.Errorf("share must be an existing directory other than /: %s", path)
	}
	return absolute, nil
}

// share adds a mount through Lima's editor, preserving the VM disk and all
// unrelated configuration. It never stops running workloads implicitly.
func (m *Machine) share(ctx context.Context, path string, stdout io.Writer) error {
	path, err := sharedDirectory(path)
	if err != nil {
		return err
	}
	lock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	instance, err := m.instance(ctx)
	if err != nil {
		return err
	}
	if instance == nil {
		return fmt.Errorf("runtime machine is not initialized; create it with mdocker machine init --mount %s", quote(path))
	}
	for _, mount := range instance.Config.Mounts {
		if within(mount.Location, path) && mount.Writable {
			_, err := fmt.Fprintf(stdout, "Already shared: %s\n", path)
			return err
		}
		if within(mount.Location, path) || within(path, mount.Location) {
			return fmt.Errorf("shared directory overlaps an existing share: %s and %s; choose a separate directory", path, mount.Location)
		}
	}
	if instance.Status != "Stopped" {
		return fmt.Errorf("stop the runtime machine before adding a share: mdocker machine stop (terminates running containers and preserves their files); then retry mdocker machine share %s and run mdocker machine start", quote(path))
	}
	mounts, err := json.Marshal([]map[string]any{{"location": path, "mountPoint": path, "writable": true}})
	if err != nil {
		return err
	}
	if _, err := m.command(ctx, "edit", "--set", ".mounts += "+string(mounts), Name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Shared: %s\nStart the runtime machine with: mdocker machine start\n", path)
	return err
}
