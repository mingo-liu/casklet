//go:build linux

package rootfs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

// Read source files before pivot_root. No bytes enter the durable root, argv or
// execution config; source descriptors are closed before any workload begins.
func readSecrets(secrets []config.Secret) (values [][]byte, resultErr error) {
	defer func() {
		if resultErr != nil {
			for _, data := range values {
				clear(data)
			}
		}
	}()
	values = make([][]byte, 0, len(secrets))
	total := 0
	for _, secret := range secrets {
		fd, err := unix.Open(secret.Source, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return values, fmt.Errorf("open secret source for %s: %w", secret.Target, err)
		}
		file := os.NewFile(uintptr(fd), "secret")
		info, err := file.Stat()
		if err == nil && (!info.Mode().IsRegular() || info.Size() > config.MaxSecretBytes) {
			err = errors.New("secret source must be a regular file of at most 1 MiB")
		}
		var data []byte
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(file, config.MaxSecretBytes+1))
		}
		err = errors.Join(err, file.Close())
		if err != nil {
			clear(data)
			return values, fmt.Errorf("read secret source for %s: %w", secret.Target, err)
		}
		total += len(data)
		if len(data) > config.MaxSecretBytes || total > 8<<20 {
			clear(data)
			return values, errors.New("secret files exceed 1 MiB per file or 8 MiB total")
		}
		values = append(values, data)
	}
	return values, nil
}

func mountSecrets(secrets []config.Secret, values [][]byte) error {
	if len(secrets) == 0 {
		return nil
	}
	target, err := openDirectory(config.SecretDirectory, true)
	if err != nil {
		return err
	}
	target.Close()
	flags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if err := unix.Mount("tmpfs", config.SecretDirectory, "tmpfs", flags, "size=9m,mode=0755"); err != nil {
		return fmt.Errorf("mount secrets: %w", err)
	}
	for i, secret := range secrets {
		name := filepath.Join(config.SecretDirectory, secret.Target)
		if err := os.WriteFile(name, values[i], 0444); err != nil {
			return errors.New("write private secret file failed")
		}
		if err := os.Chmod(name, 0444); err != nil {
			return errors.New("set private secret permissions failed")
		}
		clear(values[i])
	}
	if err := unix.Mount("", config.SecretDirectory, "tmpfs", flags|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("make secrets read-only: %w", err)
	}
	return nil
}

// ValidateSecretSources checks availability without retaining secret bytes.
func ValidateSecretSources(secrets []config.Secret) error {
	values, err := readSecrets(secrets)
	for _, data := range values {
		clear(data)
	}
	return err
}
