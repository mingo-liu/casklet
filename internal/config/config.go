package config

import "time"

// Config contains the validated options for one foreground container.
type Config struct {
	RootFS    string        `json:"rootfs"`
	Hostname  string        `json:"hostname"`
	Memory    int64         `json:"memory"`
	PidsLimit int64         `json:"pids_limit"`
	Timeout   time.Duration `json:"timeout"`
	Command   []string      `json:"command"`
}
