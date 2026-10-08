package rootfs

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
)

// ConfigureDNS replaces the resolver in the private copy, before mounting it
// read-only. Rename replaces leaf symlinks and hardlinks without modifying their targets.
func ConfigureDNS(path string, servers []string) error {
	if err := config.ValidateNetwork("bridge", servers, nil, nil); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir("etc", 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := root.Lstat("etc")
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("DNS requires /etc to be a real directory")
	}
	// Reclaim this reserved staging name after an interrupted startup. Remove
	// unlinks leaf symlinks and hardlinks without changing their targets.
	if err := root.Remove("etc/.casklet-resolv"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale resolver staging file: %w", err)
	}
	file, err := root.OpenFile("etc/.casklet-resolv", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("create container resolver: %w", err)
	}
	defer root.Remove("etc/.casklet-resolv")
	var contents strings.Builder
	for _, server := range servers {
		fmt.Fprintf(&contents, "nameserver %s\n", server)
	}
	contents.WriteString("options timeout:2 attempts:2\n")
	_, writeErr := file.WriteString(contents.String())
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return root.Rename("etc/.casklet-resolv", "etc/resolv.conf")
}
