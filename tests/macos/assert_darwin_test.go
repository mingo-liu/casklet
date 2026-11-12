//go:build darwin

package macos

import "testing"

func requireType[T any](t *testing.T, value any) T {
	t.Helper()
	result, ok := value.(T)
	if !ok {
		var want T
		t.Fatalf("unexpected value type %T; want %T", value, want)
	}
	return result
}
