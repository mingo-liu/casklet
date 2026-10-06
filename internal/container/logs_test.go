package container

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCaptureLogRetainsBoundedPrefixAndDrainsInput(t *testing.T) {
	limit := int64(len(logLimitMessage) + 5)
	for _, tt := range []struct {
		name      string
		input     string
		want      string
		truncated bool
	}{
		{"empty", "", "", false},
		{"small", "abc", "abc", false},
		{"exact payload", "abcde", "abcde", false},
		{"over payload", "abcdef", "abcde" + logLimitMessage, true},
		{"many chunks", strings.Repeat("x", 256*1024), "xxxxx" + logLimitMessage, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := bytes.NewReader([]byte(tt.input))
			var output bytes.Buffer
			truncated, err := captureLog(input, &output, limit)
			if err != nil || truncated != tt.truncated || output.String() != tt.want {
				t.Fatalf("capture = %q, %v, %v; want %q, %v", output.String(), truncated, err, tt.want, tt.truncated)
			}
			if input.Len() != 0 {
				t.Fatalf("left %d input bytes unread", input.Len())
			}
			if int64(output.Len()) > limit {
				t.Fatalf("retained %d bytes exceeds limit %d", output.Len(), limit)
			}
		})
	}
}

type logFailureWriter struct {
	calls   int
	failAt  int
	failure error
	short   bool
}

func (writer *logFailureWriter) Write(data []byte) (int, error) {
	writer.calls++
	if writer.calls == writer.failAt {
		if writer.short {
			return len(data) / 2, nil
		}
		return 0, writer.failure
	}
	return len(data), nil
}

func TestCaptureLogDrainsAfterWriteFailure(t *testing.T) {
	failure := errors.New("storage unavailable")
	input := bytes.NewReader(bytes.Repeat([]byte("data"), 128*1024))
	output := &logFailureWriter{failAt: 1, failure: failure}
	_, err := captureLog(input, output, MaxLogBytes)
	if !errors.Is(err, failure) {
		t.Fatalf("write failure was lost: %v", err)
	}
	if input.Len() != 0 || output.calls != 1 {
		t.Fatalf("failure did not drain without further writes: unread=%d, writes=%d", input.Len(), output.calls)
	}
}

func TestCaptureLogRejectsShortWrites(t *testing.T) {
	for _, tt := range []struct {
		name   string
		limit  int64
		failAt int
	}{
		{"payload", MaxLogBytes, 1},
		{"marker", int64(len(logLimitMessage) + 5), 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := bytes.NewReader(bytes.Repeat([]byte("x"), 128*1024))
			output := &logFailureWriter{failAt: tt.failAt, short: true}
			_, err := captureLog(input, output, tt.limit)
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("short write error = %v; want %v", err, io.ErrShortWrite)
			}
			if input.Len() != 0 || output.calls != tt.failAt {
				t.Fatalf("short write did not drain: unread=%d, writes=%d", input.Len(), output.calls)
			}
		})
	}
}

type logFinalErrorReader struct {
	data []byte
	err  error
}

func (reader *logFinalErrorReader) Read(buffer []byte) (int, error) {
	n := copy(buffer, reader.data)
	reader.data = reader.data[n:]
	if len(reader.data) == 0 {
		return n, reader.err
	}
	return n, nil
}

func TestCaptureLogPreservesFinalBytesAndErrors(t *testing.T) {
	readFailure := errors.New("input failed")
	writeFailure := errors.New("output failed")
	var output bytes.Buffer
	_, err := captureLog(&logFinalErrorReader{data: []byte("last bytes"), err: readFailure}, &output, MaxLogBytes)
	if !errors.Is(err, readFailure) || output.String() != "last bytes" {
		t.Fatalf("final bytes or read failure lost: %q, %v", output.String(), err)
	}
	_, err = captureLog(&logFinalErrorReader{data: []byte("last bytes"), err: readFailure}, &logFailureWriter{failAt: 1, failure: writeFailure}, MaxLogBytes)
	if !errors.Is(err, readFailure) || !errors.Is(err, writeFailure) {
		t.Fatalf("combined errors lost: %v", err)
	}
	output.Reset()
	_, err = captureLog(&logFinalErrorReader{data: []byte("last bytes"), err: io.EOF}, &output, MaxLogBytes)
	if err != nil || output.String() != "last bytes" {
		t.Fatalf("final bytes with EOF lost: %q, %v", output.String(), err)
	}
}

func TestCaptureLogRejectsUnusableLimit(t *testing.T) {
	input := bytes.NewReader([]byte("unchanged"))
	if _, err := captureLog(input, io.Discard, int64(len(logLimitMessage)-1)); err == nil {
		t.Fatal("accepted a limit smaller than the truncation marker")
	}
	if input.Len() != len("unchanged") {
		t.Fatal("consumed input for an invalid limit")
	}
}

func TestCopyInitialLogTailAndSnapshotPosition(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
		tail int
		want string
	}{
		{"entire", "one\ntwo\nthree\n", -1, "one\ntwo\nthree\n"},
		{"no initial lines", "one\ntwo\n", 0, ""},
		{"one terminated", "one\ntwo\nthree\n", 1, "three\n"},
		{"two terminated", "one\ntwo\nthree\n", 2, "two\nthree\n"},
		{"one unterminated", "one\ntwo\nthree", 1, "three"},
		{"two unterminated", "one\ntwo\nthree", 2, "two\nthree"},
		{"single newline", "\n", 1, "\n"},
		{"trailing empty line", "one\n\n", 1, "\n"},
		{"more lines than available", "one\ntwo\n", 20, "one\ntwo\n"},
		{"empty", "", 3, ""},
		{"long line", strings.Repeat("x", 128*1024), 1, strings.Repeat("x", 128*1024)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			file := strings.NewReader(tt.data + "later\n")
			var output bytes.Buffer
			if err := copyInitialLog(file, int64(len(tt.data)), tt.tail, &output); err != nil {
				t.Fatal(err)
			}
			if output.String() != tt.want {
				t.Fatalf("tail output = %q; want %q", output.String(), tt.want)
			}
			rest, err := io.ReadAll(file)
			if err != nil || string(rest) != "later\n" {
				t.Fatalf("follow position = %q, %v", rest, err)
			}
		})
	}
}

func TestCopyInitialLogPropagatesWriteFailure(t *testing.T) {
	failure := errors.New("output closed")
	for _, tail := range []int{-1, 1} {
		file := strings.NewReader("one\ntwo\n")
		if err := copyInitialLog(file, 8, tail, &logFailureWriter{failAt: 1, failure: failure}); !errors.Is(err, failure) {
			t.Fatalf("tail %d write error = %v", tail, err)
		}
	}
}
