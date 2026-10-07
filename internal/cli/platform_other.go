//go:build !darwin

package cli

import "os"

func platformArguments(args []string) []string                                    { return args }
func platformUsage() string                                                       { return usage }
func executeHostCommand([]string, *os.File, *os.File, *os.File) (bool, int)       { return false, 0 }
func executePlatform([]string, Request, *os.File, *os.File, *os.File) (bool, int) { return false, 0 }
