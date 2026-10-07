package container

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCaptureLogDrainsAllOutput(t *testing.T) {
	want := bytes.Repeat([]byte("data"), 128*1024)
	input := bytes.NewReader(want)
	var output bytes.Buffer
	if err := captureLog(input, &output); err != nil {
		t.Fatal(err)
	}
	if input.Len() != 0 || !bytes.Equal(output.Bytes(), want) {
		t.Fatal("capture lost output")
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
	err := captureLog(input, output)
	if !errors.Is(err, failure) {
		t.Fatalf("write failure was lost: %v", err)
	}
	if input.Len() != 0 || output.calls != 1 {
		t.Fatalf("failure did not drain without further writes: unread=%d, writes=%d", input.Len(), output.calls)
	}
}

func TestCaptureLogRejectsShortWrites(t *testing.T) {
	input := bytes.NewReader(bytes.Repeat([]byte("x"), 128*1024))
	output := &logFailureWriter{failAt: 1, short: true}
	if err := captureLog(input, output); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
	if input.Len() != 0 || output.calls != 1 {
		t.Fatal("did not drain after short write")
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
	err := captureLog(&logFinalErrorReader{data: []byte("last bytes"), err: readFailure}, &output)
	if !errors.Is(err, readFailure) || output.String() != "last bytes" {
		t.Fatalf("final bytes or read failure lost: %q, %v", output.String(), err)
	}
	err = captureLog(&logFinalErrorReader{data: []byte("last bytes"), err: readFailure}, &logFailureWriter{failAt: 1, failure: writeFailure})
	if !errors.Is(err, readFailure) || !errors.Is(err, writeFailure) {
		t.Fatalf("combined errors lost: %v", err)
	}
	output.Reset()
	err = captureLog(&logFinalErrorReader{data: []byte("last bytes"), err: io.EOF}, &output)
	if err != nil || output.String() != "last bytes" {
		t.Fatalf("final bytes with EOF lost: %q, %v", output.String(), err)
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
		{"long terminated line", "first\n" + strings.Repeat("x", 128*1024) + "\n", 1, strings.Repeat("x", 128*1024) + "\n"},
		{"newline at block start", "first\n" + strings.Repeat("x", 32*1024-1) + "\n", 1, strings.Repeat("x", 32*1024-1) + "\n"},
		{"newline across blocks", "first\n" + strings.Repeat("x", 32*1024) + "\n", 1, strings.Repeat("x", 32*1024) + "\n"},
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

type countingLogReader struct {
	*bytes.Reader
	readBytes int
}

func (reader *countingLogReader) Read(data []byte) (int, error) {
	n, err := reader.Reader.Read(data)
	reader.readBytes += n
	return n, err
}

func TestCopyInitialLogTailReadsOnlySuffix(t *testing.T) {
	data := bytes.Repeat([]byte("old entry\n"), 100000)
	data = append(data, []byte("last entry\n")...)
	file := &countingLogReader{Reader: bytes.NewReader(data)}
	var output bytes.Buffer
	if err := copyInitialLog(file, int64(len(data)), 1, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "last entry\n" {
		t.Fatalf("tail = %q", output.String())
	}
	if file.readBytes > 32*1024+len("last entry\n") {
		t.Fatalf("read %d bytes to tail one short line", file.readBytes)
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

func BenchmarkCopyInitialLogTail(b *testing.B) {
	data := bytes.Repeat([]byte("log entry\n"), (16<<20)/10)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := copyInitialLog(bytes.NewReader(data), int64(len(data)), 10, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}
