package container

import (
	"bytes"
	"errors"
	"io"
)

const logLimitMessage = "\nmini-docker: log limit reached; remaining output was discarded.\n"

// captureLog retains a bounded prefix and keeps draining after the limit or a
// storage failure, so an unattended command cannot block forever on its output.
func captureLog(input io.Reader, output io.Writer, limit int64) (bool, error) {
	if limit < int64(len(logLimitMessage)) {
		return false, errors.New("log limit is too small")
	}
	remaining := limit - int64(len(logLimitMessage))
	buffer := make([]byte, 32*1024)
	truncated := false
	var writeErr error
	for {
		n, readErr := input.Read(buffer)
		if n > 0 && writeErr == nil {
			keep := int64(n)
			if keep > remaining {
				keep = remaining
			}
			if keep > 0 {
				written, err := output.Write(buffer[:keep])
				writeErr = err
				if written != int(keep) && writeErr == nil {
					writeErr = io.ErrShortWrite
				}
				remaining -= keep
			}
			if int64(n) > keep && !truncated {
				truncated = true
				if writeErr == nil {
					written, err := io.WriteString(output, logLimitMessage)
					writeErr = err
					if written != len(logLimitMessage) && writeErr == nil {
						writeErr = io.ErrShortWrite
					}
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				readErr = nil
			}
			return truncated, errors.Join(writeErr, readErr)
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
	// At most 16 MiB is retained. Reading this bounded snapshot preserves exact
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
