package container

import (
	"bytes"
	"errors"
	"io"
)

// captureLog continues draining after storage failure so a workload cannot block
// indefinitely on its output. Rotation is handled by the destination writer.
func captureLog(input io.Reader, output io.Writer) error {
	buffer := make([]byte, 32*1024)
	var writeErr error
	for {
		n, readErr := input.Read(buffer)
		if n > 0 && writeErr == nil {
			written, err := output.Write(buffer[:n])
			writeErr = err
			if written != n && writeErr == nil {
				writeErr = io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				readErr = nil
			}
			return errors.Join(writeErr, readErr)
		}
	}
}

func copyInitialLog(file io.ReadSeeker, length int64, tail int, out io.Writer) error {
	if tail == -1 {
		_, err := io.CopyN(out, file, length)
		return err
	}
	if tail == 0 {
		_, err := file.Seek(length, io.SeekStart)
		return err
	}
	// At most 64 MiB is retained. Reading this bounded snapshot preserves exact
	// newline handling for tails, including a final unterminated line.
	data, err := io.ReadAll(io.LimitReader(file, length))
	if err != nil {
		return err
	}
	end, start := len(data), 0
	if end > 0 && data[end-1] == '\n' {
		end--
	}
	for i := end - 1; i >= 0; i-- {
		if data[i] == '\n' {
			tail--
			if tail == 0 {
				start = i + 1
				break
			}
		}
	}
	_, err = io.Copy(out, bytes.NewReader(data[start:]))
	return err
}
