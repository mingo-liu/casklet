//go:build !linux && !darwin

package main

func remoteMode([]string) (bool, int) { return false, 0 }
