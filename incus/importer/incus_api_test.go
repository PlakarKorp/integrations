package importer

import (
	"bytes"
	"io"
	"testing"
)

// newTestPipeWriteSeeker wires a pipeWriteSeeker to an in-memory io.Reader
// via an io.Pipe, draining the reader in the background so Write calls on
// the sink never block. The returned closeAndWait func closes the pipe and
// blocks until the drain goroutine has copied everything into got, so the
// buffer is safe to inspect once it returns.
func newTestPipeWriteSeeker(t *testing.T) (sink *pipeWriteSeeker, got *bytes.Buffer, closeAndWait func()) {
	t.Helper()

	pr, pw := io.Pipe()
	sink = &pipeWriteSeeker{w: pw}
	got = &bytes.Buffer{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(got, pr)
	}()

	closeAndWait = func() {
		_ = pw.Close()
		<-done
	}
	t.Cleanup(closeAndWait)

	return sink, got, closeAndWait
}

func TestPipeWriteSeekerWriteAdvancesOffset(t *testing.T) {
	sink, got, closeAndWait := newTestPipeWriteSeeker(t)

	n, err := sink.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Fatalf("Write returned n=%d, want 5", n)
	}
	if sink.offset != 5 {
		t.Fatalf("offset after first write = %d, want 5", sink.offset)
	}

	n, err = sink.Write([]byte(" world"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 6 {
		t.Fatalf("Write returned n=%d, want 6", n)
	}
	if sink.offset != 11 {
		t.Fatalf("offset after second write = %d, want 11", sink.offset)
	}

	closeAndWait()
	if want := "hello world"; got.String() != want {
		t.Fatalf("underlying writer got %q, want %q", got.String(), want)
	}
}

func TestPipeWriteSeekerSeekCurrentReturnsOffset(t *testing.T) {
	sink, _, _ := newTestPipeWriteSeeker(t)

	if _, err := sink.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	off, err := sink.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(0, io.SeekCurrent): unexpected error: %v", err)
	}
	if off != 4 {
		t.Fatalf("Seek(0, io.SeekCurrent) = %d, want 4", off)
	}
}

func TestPipeWriteSeekerRejectsUnsupportedSeeks(t *testing.T) {
	cases := []struct {
		name   string
		offset int64
		whence int
	}{
		{"SeekStart zero", 0, io.SeekStart},
		{"SeekCurrent nonzero", 5, io.SeekCurrent},
		{"SeekEnd zero", 0, io.SeekEnd},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink, _, _ := newTestPipeWriteSeeker(t)

			if _, err := sink.Seek(tc.offset, tc.whence); err == nil {
				t.Fatalf("Seek(%d, %d) = nil error, want error", tc.offset, tc.whence)
			}
		})
	}
}
