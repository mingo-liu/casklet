package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseStopSignal uses Linux signal numbers on every host, including Darwin.
func ParseStopSignal(value string) (int, error) {
	if value != "" && strings.Trim(value, "0123456789") == "" {
		n, err := strconv.Atoi(value)
		if err == nil && n >= 1 && n <= 64 {
			return n, nil
		}
	}
	name := strings.TrimPrefix(strings.ToUpper(value), "SIG")
	signals := map[string]int{"HUP": 1, "INT": 2, "QUIT": 3, "ILL": 4, "TRAP": 5, "ABRT": 6, "IOT": 6, "BUS": 7, "FPE": 8, "KILL": 9, "USR1": 10, "SEGV": 11, "USR2": 12, "PIPE": 13, "ALRM": 14, "TERM": 15, "STKFLT": 16, "CHLD": 17, "CLD": 17, "CONT": 18, "STOP": 19, "TSTP": 20, "TTIN": 21, "TTOU": 22, "URG": 23, "XCPU": 24, "XFSZ": 25, "VTALRM": 26, "PROF": 27, "WINCH": 28, "IO": 29, "POLL": 29, "PWR": 30, "SYS": 31}
	if n, ok := signals[name]; ok {
		return n, nil
	}
	return 0, fmt.Errorf("invalid Linux stop signal %q; use a signal name such as SIGTERM or a number from 1 to 64", value)
}
func (c Config) StoppingSignal() int {
	if c.StopSignal == "" {
		return 15
	}
	n, _ := ParseStopSignal(c.StopSignal)
	return n
}
func (c Config) StoppingSignalName() string {
	if c.StopSignal == "" {
		return "SIGTERM"
	}
	return c.StopSignal
}
