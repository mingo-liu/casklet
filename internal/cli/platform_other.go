//go:build !darwin

package cli

import (
	"fmt"
	"os"
)

func platformArguments(args []string) []string { return args }
func platformUsage(topic string) (string, error) {
	if hostHelpTopic(topic) {
		return "", fmt.Errorf("help topic %q is available only on macOS; run mdocker help for supported commands", topic)
	}
	return scopedUsage(topic, false)
}
func executeHostCommand([]string, *os.File, *os.File, *os.File) (bool, int)       { return false, 0 }
func executePlatform([]string, Request, *os.File, *os.File, *os.File) (bool, int) { return false, 0 }
