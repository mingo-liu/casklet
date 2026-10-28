package config

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

const SecretDirectory = "/run/secrets"
const MaxSecretBytes = 1 << 20

// Secret records a source location, never its contents. Each execution reads a
// fresh bounded snapshot into its private read-only tmpfs.
type Secret struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

var secretName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func ParseSecret(value, base string) (Secret, error) {
	var secret Secret
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		key, val, ok := strings.Cut(part, "=")
		if !ok || seen[key] {
			return secret, errors.New("secret requires source=FILE,target=NAME")
		}
		seen[key] = true
		switch key {
		case "source":
			secret.Source = val
		case "target":
			secret.Target = val
		default:
			return secret, errors.New("unknown secret option")
		}
	}
	if base != "" && secret.Source != "" && !filepath.IsAbs(secret.Source) {
		secret.Source = filepath.Join(base, secret.Source)
	}
	return secret, ValidateSecrets([]Secret{secret}, nil)
}

func ValidateSecrets(secrets []Secret, mounts []BindMount) error {
	if len(secrets) > 32 {
		return errors.New("at most 32 secret files are supported")
	}
	seen := map[string]bool{}
	for _, secret := range secrets {
		if !filepath.IsAbs(secret.Source) || filepath.Clean(secret.Source) != secret.Source || strings.ContainsAny(secret.Source, "\x00\r\n,") || !secretName.MatchString(secret.Target) || seen[secret.Target] {
			return errors.New("secret requires a clean absolute source and a unique 1-128 character file name target")
		}
		seen[secret.Target] = true
	}
	if len(secrets) > 0 {
		for _, mount := range mounts {
			if pathsOverlap(mount.Target, SecretDirectory) {
				return errors.New("mount target overlaps /run/secrets")
			}
		}
	}
	return nil
}

func (s Secret) Argument() string { return "source=" + s.Source + ",target=" + s.Target }
