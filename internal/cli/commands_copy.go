package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/copyfs"
)

func executeCopy(r Request, stderr *os.File) int {
	return manageOperation(r, stderr, func(ctx context.Context) (int, error) { return copyHost(ctx, r, stderr, platformCopy) })
}

type copyTransport func(context.Context, []string, *os.File, *os.File, *os.File) (int, error)

// Local paths stay on the invoking host and require no VM share.
func copyHost(ctx context.Context, r Request, stderr *os.File, transport copyTransport) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	toContainer := r.CopyTarget.Container != ""
	local, remote := r.CopyTarget, r.CopySource
	if toContainer {
		local, remote = r.CopySource, r.CopyTarget
	}
	filename, err := filepath.Abs(local.Path)
	if err != nil {
		return 125, err
	}
	root, err := os.OpenRoot(filepath.Dir(filename))
	if err != nil {
		return 125, err
	}
	defer root.Close()
	name := filepath.Base(filename)
	if !toContainer && strings.HasSuffix(local.Path, "/") {
		name += "/"
	}
	if toContainer && strings.HasSuffix(local.Path, "/.") {
		name += "/."
	}
	if toContainer {
		if _, err := root.Lstat(strings.TrimSuffix(name, "/.")); err != nil {
			return 125, err
		}
	}
	input, output, err := os.Pipe()
	if err != nil {
		return 125, err
	}
	defer input.Close()
	defer output.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 125, err
	}
	defer null.Close()
	archiveEnd := input
	if toContainer {
		archiveEnd = output
	}
	stream, finish, err := cancelableStreamFile(ctx, archiveEnd)
	if err != nil {
		return 125, err
	}
	defer finish()
	archiveDone := make(chan error, 1)
	go func() {
		var err error
		if toContainer {
			err = copyfs.Export(ctx, root, name, stream)
		} else {
			err = copyfs.Import(ctx, root, name, stream)
		}
		stream.Close()
		archiveEnd.Close()
		if err != nil {
			cancel()
		}
		archiveDone <- err
	}()
	action := "__copy-out"
	stdin, stdout := null, output
	if toContainer {
		action, stdin, stdout = "__copy-in", input, null
	}
	code, transportErr := transport(ctx, []string{action, remote.Container, remote.Path}, stdin, stdout, stderr)
	if toContainer {
		input.Close()
	} else {
		output.Close()
	}
	if transportErr != nil || code != 0 {
		cancel()
	}
	archiveErr := <-archiveDone
	if archiveErr != nil || transportErr != nil {
		return 125, errors.Join(archiveErr, transportErr)
	}
	return code, nil
}

func executeCopyStream(ctx context.Context, r Request, stdout io.Writer, input ...io.Reader) (int, error) {
	in := os.Stdin
	if len(input) > 0 {
		var ok bool
		in, ok = input[0].(*os.File)
		if !ok {
			return 125, errors.New("copy requires a file stream")
		}
	}
	out, ok := stdout.(*os.File)
	if !ok {
		return 125, errors.New("copy requires a file stream")
	}
	return 0, container.Copy(ctx, r.Reference, containerCopyPath(r.CopyPath), r.Action == "copy-in", in, out, os.Stderr)
}
