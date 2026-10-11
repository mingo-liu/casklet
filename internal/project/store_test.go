package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectStorePreservesReceiptsAndCancelsLockWait(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	receipt := Resource{Kind: "volume", Name: "cp-demo-data", CreatedAt: time.Now().UTC()}
	state.Resources["volume:"+receipt.Name] = receipt
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || !sameResource(loaded.Resources["volume:"+receipt.Name], receipt) {
		t.Fatalf("receipt %v %v", loaded, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if other, err := Open(ctx, root, "demo"); !errors.Is(err, context.DeadlineExceeded) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("lock wait %v", err)
	}
	if err := store.Save(State{Name: "other", Version: 1}); err == nil {
		t.Fatal("accepted unrelated state")
	}
}

func TestProjectStoreRejectsUnsafeOrMismatchedArtifacts(t *testing.T) {
	for _, kind := range []string{"directory-symlink", "state-symlink", "lock-symlink", "world-readable", "wrong-project", "wrong-resource"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(target, []byte("sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory-symlink":
				root = filepath.Join(root, "link")
				if err := os.Symlink(filepath.Dir(target), root); err != nil {
					t.Fatal(err)
				}
			case "state-symlink":
				os.Symlink(target, filepath.Join(root, "demo.json"))
			case "lock-symlink":
				os.Symlink(target, filepath.Join(root, ".demo.lock"))
			case "world-readable":
				os.WriteFile(filepath.Join(root, "demo.json"), []byte(`{}`), 0644)
			case "wrong-project":
				os.WriteFile(filepath.Join(root, "demo.json"), []byte(`{"version":1,"name":"other","resources":{}}`), 0600)
			case "wrong-resource":
				os.WriteFile(filepath.Join(root, "demo.json"), []byte(`{"version":1,"name":"demo","resources":{"volume:bad":{"kind":"network","name":"bad"}}}`), 0600)
			}
			store, err := Open(context.Background(), root, "demo")
			if err == nil {
				defer store.Close()
				_, err = store.Load()
			}
			if err == nil {
				t.Fatal("accepted unsafe state")
			}
			contents, _ := os.ReadFile(target)
			if string(contents) != "sentinel" {
				t.Fatal("mutated symlink target")
			}
		})
	}
}
