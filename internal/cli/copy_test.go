package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/copyfs"
)

func TestCopyParsingAndFocusedHelp(t *testing.T) {
	for _, args := range [][]string{{"cp", "local", "worker:/etc/value"}, {"cp", "abcd:etc/value", "./a:b"}, {"cp", "--", "-file", "worker:/value"}, {"cp", "worker:/data/.", "."}} {
		r, err := Parse(args)
		if err != nil || r.Action != "cp" {
			t.Fatalf("%v: %+v %v", args, r, err)
		}
	}
	for _, args := range [][]string{{"cp"}, {"cp", "one"}, {"cp", "one", "two"}, {"cp", "a:/one", "b:/two"}, {"cp", ":/one", "two"}, {"cp", "a:", "two"}, {"cp", "a:/../one", "two"}, {"cp", "a:/one", "-"}, {"cp", "--archive", "a:/one", "two"}, {"cp", "a:/one", "two", "three"}} {
		if _, err := Parse(args); err == nil || !strings.Contains(err.Error(), "casklet cp --help") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"__copy-in", "id", "/../escape"}, {"__copy-out", "id"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if got := containerCopyPath("/data/."); got != "data/." {
		t.Fatal(got)
	}
}

func TestCopyHostUsesStreamsWithoutSharedPaths(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source ' $ file")
	os.WriteFile(source, []byte("binary\x00value"), 0640)
	guest := t.TempDir()
	root, err := os.OpenRoot(guest)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	transport := func(ctx context.Context, args []string, in, out, stderr *os.File) (int, error) {
		if len(args) != 3 || args[1] != "worker" {
			t.Fatalf("args %v", args)
		}
		if args[0] == "__copy-in" {
			return 0, copyfs.Import(ctx, root, strings.TrimPrefix(args[2], "/"), in)
		}
		return 0, copyfs.Export(ctx, root, strings.TrimPrefix(args[2], "/"), out)
	}
	for _, args := range [][]string{{"cp", source, "worker:/uploaded"}, {"cp", "worker:/uploaded", filepath.Join(dir, "downloaded")}} {
		r, err := Parse(args)
		if err != nil {
			t.Fatal(err)
		}
		if code, err := copyHost(context.Background(), r, null, transport); err != nil || code != 0 {
			t.Fatalf("copy %v: %d %v", args, code, err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "downloaded"))
	if string(got) != "binary\x00value" {
		t.Fatalf("round trip %q", got)
	}
}

func TestCopyHostCancelsBlockedStreamsAndPreparationFailure(t *testing.T) {
	null, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	defer null.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "large")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(2 << 20)
	f.Close()
	for _, upload := range []bool{false, true} {
		t.Run(map[bool]string{false: "download", true: "upload"}[upload], func(t *testing.T) {
			args := []string{"cp", "worker:/file", filepath.Join(dir, "out")}
			if upload {
				args = []string{"cp", file, "worker:/file"}
			}
			r, err := Parse(args)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err = copyHost(ctx, r, null, func(ctx context.Context, _ []string, _, _, _ *os.File) (int, error) {
				<-ctx.Done()
				return 125, ctx.Err()
			})
			if err == nil || time.Since(start) > 2*time.Second {
				t.Fatalf("blocked copy: %v", err)
			}
			sentinel := errors.New("preparation failed")
			_, err = copyHost(context.Background(), r, null, func(context.Context, []string, *os.File, *os.File, *os.File) (int, error) { return 125, sentinel })
			if !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
		})
	}
}

func TestCopyHostOutputFailureCancelsTransport(t *testing.T) {
	null, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	defer null.Close()
	r, err := Parse([]string{"cp", "worker:/file", filepath.Join(t.TempDir(), "missing", "out")})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = copyHost(context.Background(), r, null, func(context.Context, []string, *os.File, *os.File, *os.File) (int, error) { calls++; return 0, nil })
	if err == nil || calls != 0 {
		t.Fatalf("destination preflight %v %d", err, calls)
	}
}
