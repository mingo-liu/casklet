//go:build !darwin

package cli

func platformEngineSHA256() (string, error) { return "", nil }
