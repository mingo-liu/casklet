//go:build linux

package container

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkLogSnapshot(b *testing.B) {
	benchmarkLogSnapshot(b, -1)
}

func BenchmarkInitialLogTail(b *testing.B) {
	for _, tail := range []int{0, 20} {
		b.Run(fmt.Sprintf("tail-%d", tail), func(b *testing.B) { benchmarkLogSnapshot(b, tail) })
	}
}

func benchmarkLogSnapshot(b *testing.B, tail int) {
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
	segment := bytes.Repeat([]byte("entry xxxxxxxxx\n"), size/16)
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(store.root, record.ID, logName(i)), segment, 0600); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.SetBytes(size * files)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, cursor, err := store.logSnapshotTail(context.Background(), record.ID, cfg, logCursor{}, record.Generation, tail)
		if cursor.pin != nil {
			cursor.pin.Close()
		}
		want := size * files
		if tail >= 0 {
			want = 16 * tail
		}
		if err != nil || len(data) != want {
			b.Fatalf("snapshot: bytes=%d err=%v", len(data), err)
		}
	}
}
