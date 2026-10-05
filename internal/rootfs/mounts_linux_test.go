//go:build linux

package rootfs

import "testing"

func TestDecodeMountPath(t *testing.T) {
	if got := decodeMountPath(`/a\040b\011c\012d\134e`); got != "/a b\tc\nd\\e" {
		t.Fatalf("incorrect mount path decoding: %q", got)
	}
}
