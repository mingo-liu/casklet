package image

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func BenchmarkIdentitySmallFiles(b *testing.B) {
	root := b.TempDir()
	const files = 256
	data := make([]byte, 1024)
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d", i)), data, 0644); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.SetBytes(files * int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, size, err := Identity(context.Background(), root, runtime.GOARCH)
		if err != nil || ValidateID(id) != nil || size != files*int64(len(data)) {
			b.Fatalf("identity: %q %d %v", id, size, err)
		}
	}
}
