package cli

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
)

var cpuPattern = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]{1,3})?|\.[0-9]{1,3})$`)
var identityPattern = regexp.MustCompile(`^[0-9]+$`)

func ParseMemory(value string) (int64, error) {
	original := value
	value = strings.ToLower(value)
	multiplier := int64(1)
	if len(value) > 0 {
		switch value[len(value)-1] {
		case 'k':
			multiplier = 1 << 10
		case 'm':
			multiplier = 1 << 20
		case 'g':
			multiplier = 1 << 30
		}
		if multiplier != 1 {
			value = value[:len(value)-1]
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("invalid memory limit %q: use a positive integer with an optional k, m, or g suffix", original)
	}
	return n * multiplier, nil
}

// ParseCPUs converts a decimal CPU count to an exact cgroups v2 quota.
func ParseCPUs(value string) (int64, error) {
	invalid := func() (int64, error) {
		return 0, fmt.Errorf("invalid CPU limit %q: use 0 or 0.01-1000 cores with up to 3 decimal places", value)
	}
	if !cpuPattern.MatchString(value) {
		return invalid()
	}
	whole, fractional, _ := strings.Cut(value, ".")
	if whole == "" {
		whole = "0"
	}
	cores, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || cores > 1000 {
		return invalid()
	}
	fractional += strings.Repeat("0", 3-len(fractional))
	fraction, err := strconv.ParseInt(fractional, 10, 64)
	if err != nil {
		return invalid()
	}
	quota := cores*config.CPUPeriod + fraction*100
	if quota > config.MaxCPUQuota || (quota > 0 && quota < config.MinCPUQuota) {
		return invalid()
	}
	return quota, nil
}

// ParseUser accepts only numeric IDs; it does not consult host user databases.
func ParseUser(value string) (*config.User, error) {
	uidText, gidText, hasGroup := strings.Cut(value, ":")
	if !hasGroup {
		gidText = uidText
	}
	parseID := func(text string) (uint32, error) {
		if !identityPattern.MatchString(text) {
			return 0, errors.New("invalid numeric ID")
		}
		id, err := strconv.ParseUint(text, 10, 32)
		if err != nil || id == math.MaxUint32 {
			return 0, errors.New("numeric ID is out of range")
		}
		return uint32(id), nil
	}
	uid, uidErr := parseID(uidText)
	gid, gidErr := parseID(gidText)
	if uidErr != nil || gidErr != nil {
		return nil, fmt.Errorf("invalid user %q: use numeric UID[:GID] between 0 and 4294967294", value)
	}
	return &config.User{UID: uid, GID: gid}, nil
}
