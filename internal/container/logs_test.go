package container

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCaptureLogDrainsAllOutput(t *testing.T) {
	var (
		want   = bytes.Repeat([]byte("data"), 128*1024)
		input  = bytes.NewReader(want)
		output bytes.Buffer
	)
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
	var (
		failure = errors.New("storage unavailable")
		input   = bytes.NewReader(bytes.Repeat([]byte("data"), 128*1024))
		output  = &logFailureWriter{failAt: 1, failure: failure}
		err     = captureLog(input, output)
	)
	if !errors.Is(err, failure) {
		t.Fatalf("write failure was lost: %v", err)
	}
	if input.Len() != 0 || output.calls != 1 {
		t.Fatalf("failure did not drain without further writes: unread=%d, writes=%d", input.Len(), output.calls)
	}
}

func TestCaptureLogResumesAfterTemporaryLockFailure(t *testing.T) {
	var (
		input  = bytes.NewReader(bytes.Repeat([]byte("x"), 3*32*1024))
		output = &logFailureWriter{failAt: 1, failure: errLogBusy}
	)
	if err := captureLog(input, output); !errors.Is(err, errLogBusy) {
		t.Fatalf("lost contention diagnostic: %v", err)
	}
	if input.Len() != 0 || output.calls != 3 {
		t.Fatalf("capture did not resume: unread=%d, writes=%d", input.Len(), output.calls)
	}
	var (
		readFailure = errors.New("final read failed")
		err         = captureLog(&logFinalErrorReader{data: []byte("last"), err: readFailure}, &logFailureWriter{failAt: 1, failure: errLogBusy})
	)
	if !errors.Is(err, readFailure) || !errors.Is(err, errLogBusy) {
		t.Fatalf("final contention or input failure lost: %v", err)
	}
}

func TestCaptureLogRejectsShortWrites(t *testing.T) {
	var (
		input  = bytes.NewReader(bytes.Repeat([]byte("x"), 128*1024))
		output = &logFailureWriter{failAt: 1, short: true}
	)
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
	var (
		readFailure  = errors.New("input failed")
		writeFailure = errors.New("output failed")
		output       bytes.Buffer
		err          = captureLog(&logFinalErrorReader{data: []byte("last bytes"), err: readFailure}, &output)
	)
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
	tests := []struct {
		name string
		data string
		tail int
		want string
	}{
		{name: "entire", data: "one\ntwo\nthree\n", tail: -1, want: "one\ntwo\nthree\n"},
		{name: "no initial lines", data: "one\ntwo\n", tail: 0, want: ""},
		{name: "one terminated", data: "one\ntwo\nthree\n", tail: 1, want: "three\n"},
		{name: "two terminated", data: "one\ntwo\nthree\n", tail: 2, want: "two\nthree\n"},
		{name: "one unterminated", data: "one\ntwo\nthree", tail: 1, want: "three"},
		{name: "two unterminated", data: "one\ntwo\nthree", tail: 2, want: "two\nthree"},
		{name: "single newline", data: "\n", tail: 1, want: "\n"},
		{name: "trailing empty line", data: "one\n\n", tail: 1, want: "\n"},
		{name: "more lines than available", data: "one\ntwo\n", tail: 20, want: "one\ntwo\n"},
		{name: "empty", data: "", tail: 3, want: ""},
		{name: "long line", data: strings.Repeat("x", 128*1024), tail: 1, want: strings.Repeat("x", 128*1024)},
		{
			name: "long terminated line",
			data: "first\n" + strings.Repeat("x", 128*1024) + "\n",
			tail: 1,
			want: strings.Repeat("x", 128*1024) + "\n",
		},
		{
			name: "newline at block start",
			data: "first\n" + strings.Repeat("x", 32*1024-1) + "\n",
			tail: 1,
			want: strings.Repeat("x", 32*1024-1) + "\n",
		},
		{
			name: "newline across blocks",
			data: "first\n" + strings.Repeat("x", 32*1024) + "\n",
			tail: 1,
			want: strings.Repeat("x", 32*1024) + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				file   = strings.NewReader(tt.data + "later\n")
				output bytes.Buffer
			)
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
	var (
		file   = &countingLogReader{Reader: bytes.NewReader(data)}
		output bytes.Buffer
	)
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
