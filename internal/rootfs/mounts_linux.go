//go:build linux

package rootfs

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func checkMounts(source string) error { return rejectMounts(source, false) }

// CheckUnmounted refuses mounted storage before recursive deletion.
func CheckUnmounted(source string) error { return rejectMounts(source, true) }

func rejectMounts(source string, includeRoot bool) error {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("inspect rootfs mount points: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			return fmt.Errorf("invalid mountinfo entry: %q", scanner.Text())
		}
		mount := decodeMountPath(fields[4])
		if (includeRoot || mount != source) && within(source, mount) {
			return fmt.Errorf("rootfs contains a mounted subtree: %s", mount)
		}
	}
	return scanner.Err()
}

func decodeMountPath(path string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(path)
}
