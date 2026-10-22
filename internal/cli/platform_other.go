//go:build !darwin

package cli

import (
	"fmt"
	"os"
)

func platformProjectReservations(data []byte) ([]byte, error) { return data, nil }
func platformProjectRecords(data []byte) ([]byte, error)      { return data, nil }

func platformArguments(args []string) []string { return args }
func platformUsage(topic string) (string, error) {
	if hostHelpTopic(topic) {
		return "", fmt.Errorf("help topic %q is available only on macOS; run casklet help for supported commands", topic)
	}
	return scopedUsage(topic, false)
}
func executeHostCommand([]string, *os.File, *os.File, *os.File) (bool, int)       { return false, 0 }
func executePlatform([]string, Request, *os.File, *os.File, *os.File) (bool, int) { return false, 0 }
