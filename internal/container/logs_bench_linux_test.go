//go:build linux

package container

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkLogSnapshot(b *testing.B) {
	store, err := newStoreAt(filepath.Join(b.TempDir(), "containers"))
	if err != nil {
		b.Fatal(err)
	}
	cfg := testConfig()
	const size = 4 << 20
	const files = 4
	cfg.LogMaxSize, cfg.LogMaxFiles = size, files
	record, err := store.Create(context.Background(), cfg, "benchmark")
	if err != nil {
		b.Fatal(err)
	}
	segment := bytes.Repeat([]byte("x"), size)
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(store.root, record.ID, logName(i)), segment, 0600); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.SetBytes(size * files)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, cursor, err := store.logSnapshot(context.Background(), record.ID, cfg, logCursor{}, record.Generation)
		if cursor.pin != nil {
			cursor.pin.Close()
		}
		if err != nil || len(data) != size*files {
			b.Fatalf("snapshot: bytes=%d err=%v", len(data), err)
		}
	}
}
