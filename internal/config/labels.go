package config

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var labelKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)

// ValidateLabelKey accepts bounded, portable metadata keys, including namespaces.
func ValidateLabelKey(key string) error {
	if !labelKey.MatchString(key) {
		return errors.New("label key must contain 1-128 ASCII letters, digits, dots, underscores, colons, slashes, or hyphens and start alphanumeric")
	}
	return nil
}

// ParseLabel preserves literal values, including empty values and equals signs.
func ParseLabel(assignment string) (string, string, error) {
	key, value, ok := strings.Cut(assignment, "=")
	if !ok {
		return "", "", errors.New("label requires KEY=VALUE")
	}
	if err := ValidateLabels(map[string]string{key: value}); err != nil {
		return "", "", err
	}
	return key, value, nil
}

// ValidateLabels bounds listing metadata independently of the workload environment.
func ValidateLabels(labels map[string]string) error {
	if len(labels) > 64 {
		return errors.New("container labels are limited to 64 keys")
	}
	total := 0
	for key, value := range labels {
		if err := ValidateLabelKey(key); err != nil {
			return err
		}
		if len(value) > 4096 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return errors.New("label value must be valid UTF-8, contain no control characters, and be at most 4096 bytes")
		}
		total += len(key) + len(value)
	}
	if total > 16*1024 {
		return errors.New("container label keys and values are limited to 16 KiB total")
	}
	return nil
}
