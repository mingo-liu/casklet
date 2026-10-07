package container

import (
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
	// Search backwards with bounded memory, then stream only the requested
	// suffix. Ignore a final newline so it terminates the last line rather
	// than introducing an extra one. Reads stay within the initial snapshot.
	buffer := make([]byte, 32*1024)
	start := int64(0)
search:
	for end := length; end > 0; {
		begin := max(int64(0), end-int64(len(buffer)))
		if _, err := file.Seek(begin, io.SeekStart); err != nil {
			return err
		}
		chunk := buffer[:end-begin]
		if _, err := io.ReadFull(file, chunk); err != nil {
			return err
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] == '\n' && begin+int64(i) != length-1 {
				tail--
				if tail == 0 {
					start = begin + int64(i) + 1
					break search
				}
			}
		}
		end = begin
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	_, err := io.CopyN(out, file, length-start)
	return err
}
