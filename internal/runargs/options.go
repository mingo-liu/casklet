// Package runargs walks run-style options for local files and host transport.
// It owns token boundaries only; the CLI parser owns supported flags and validation.
package runargs

import (
	"fmt"
	"strings"
)

// IsBoolean includes run aliases and the doctor/help options sharing its scanner.
func IsBoolean(name string) bool {
	switch name {
	case "d", "detach", "i", "interactive", "t", "tty", "it", "ti", "read-only", "rootless", "userns", "no-healthcheck", "json", "h", "help":
		return true
	}
	return false
}

// Walk skips argv[0], visits each option once and stops before operands or --.
// Index and end are the inclusive original token span, including a separate value.
func Walk(args []string, visit func(name, value string, index, end int) error) error {
	for i := 1; i < len(args); i++ {
		if args[i] == "--" || !strings.HasPrefix(args[i], "-") || args[i] == "-" {
			break
		}
		token := strings.TrimPrefix(strings.TrimPrefix(args[i], "-"), "-")
		if token == "" || token[0] == '-' {
			break
		}
		name, value, inline := strings.Cut(token, "=")
		end := i
		if !inline && !IsBoolean(name) {
			end++
			if end >= len(args) {
				return fmt.Errorf("missing value for --%s", name)
			}
			value = args[end]
		}
		if err := visit(name, value, i, end); err != nil {
			return err
		}
		i = end
	}
	return nil
}

// SetValue preserves the original spelling and inline/separate form of a value.
func SetValue(args []string, index, end int, value string) {
	if index == end {
		name, _, _ := strings.Cut(args[index], "=")
		args[index] = name + "=" + value
	} else {
		args[end] = value
	}
}
