package storage

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// payload builds a body whose every 8-byte cell names its own offset, so a
// test can tell "the right number of bytes" from "the right bytes".
func payload(n int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < n; i += 8 {
		fmt.Fprintf(&b, "[%05d]", i)
	}
	return b.Bytes()[:n]
}

// rangeServer is a well-behaved server: it honours Range and, if gets is not
// nil, counts the GETs it serves.
func rangeServer(t *testing.T, body []byte, gets *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && gets != nil {
			*gets++
		}
		http.ServeContent(w, r, "test.ptar", time.Time{}, bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewHTTPReader(t *testing.T) {
	body := payload(4096)
	hr, err := NewHTTPReader(rangeServer(t, body, nil).URL)
	if err != nil {
		t.Fatal(err)
	}
	if hr.size != int64(len(body)) {
		t.Errorf("size = %d, want %d", hr.size, len(body))
	}
	if hr.client.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", hr.client.Timeout, defaultTimeout)
	}
}

// Without a Content-Length there is no size to seek within, so opening has to
// fail rather than leave the reader believing the file is empty.
func TestNewHTTPReaderNeedsASize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := NewHTTPReader(srv.URL); err == nil {
		t.Fatal("a server that reports no size was accepted")
	}
}

func TestReadAt(t *testing.T) {
	body := payload(4096)
	hr, err := NewHTTPReader(rangeServer(t, body, nil).URL)
	if err != nil {
		t.Fatal(err)
	}

	// a window wholly inside the file fills the buffer and reports no error
	buf := make([]byte, 100)
	n, err := hr.ReadAt(buf, 200)
	if n != 100 || err != nil {
		t.Fatalf("ReadAt(200) = %d, %v; want 100, nil", n, err)
	}
	if !bytes.Equal(buf, body[200:300]) {
		t.Errorf("ReadAt(200) returned the wrong window:\n got %q\nwant %q", buf, body[200:300])
	}

	// a window clamped by the end of the file is a short read, and io.ReaderAt
	// requires a short read to carry an error
	n, err = hr.ReadAt(buf, int64(len(body)-46))
	if n != 46 || err != io.EOF {
		t.Fatalf("ReadAt(tail) = %d, %v; want 46, io.EOF", n, err)
	}
	if !bytes.Equal(buf[:n], body[len(body)-46:]) {
		t.Errorf("ReadAt(tail) returned the wrong bytes")
	}

	// past the end there is nothing to return
	if n, err := hr.ReadAt(buf, int64(len(body))); n != 0 || err != io.EOF {
		t.Errorf("ReadAt(past end) = %d, %v; want 0, io.EOF", n, err)
	}
}

func TestReadSequential(t *testing.T) {
	body := payload(1 << 20)
	gets := 0
	hr, err := NewHTTPReader(rangeServer(t, body, &gets).URL)
	if err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(hr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("read %d bytes, want %d, and they differ", len(got), len(body))
	}

	// Each Read fills its buffer instead of returning whatever the first
	// packet carried, so the whole file costs a few dozen requests rather
	// than hundreds.  This bound is loose on purpose; before io.ReadFull the
	// same read took 628 GETs.
	if gets > 64 {
		t.Errorf("reading %d bytes took %d GETs; a single Body.Read per call is back", len(body), gets)
	}
}

// A read that asks for less than the file has left must come back full.
func TestReadFillsTheBuffer(t *testing.T) {
	body := payload(1 << 20)
	hr, err := NewHTTPReader(rangeServer(t, body, nil).URL)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 256<<10)
	n, err := hr.Read(buf)
	if n != len(buf) || err != nil {
		t.Fatalf("Read = %d, %v; want %d, nil", n, err, len(buf))
	}
	if !bytes.Equal(buf, body[:len(buf)]) {
		t.Errorf("Read returned the wrong bytes")
	}
}

// A server that acknowledges a range and then sends less than it promised is
// not at the end of the file: saying io.EOF there would silently truncate.
func TestReadTruncatedResponse(t *testing.T) {
	body := payload(4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-99/%d", len(body)))
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusPartialContent)
		// ...and then send nothing at all
	}))
	defer srv.Close()

	hr, err := NewHTTPReader(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(hr); err != io.ErrUnexpectedEOF {
		t.Errorf("ReadAll on a truncating server: err = %v, want io.ErrUnexpectedEOF", err)
	}
}

// A server that ignores Range answers 200 with the whole body.  Reading the
// head of that as though it were the requested window silently returns the
// wrong bytes, so only 206 may be treated as a range.
func TestReadAtRejectsIgnoredRange(t *testing.T) {
	body := payload(4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		if r.Method == http.MethodHead {
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer srv.Close()

	hr, err := NewHTTPReader(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hr.ReadAt(make([]byte, 100), 500); err == nil {
		t.Fatal("a 200 answer to a Range request was accepted as the range")
	}
}

func TestSeekThenRead(t *testing.T) {
	body := payload(4096)
	hr, err := NewHTTPReader(rangeServer(t, body, nil).URL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := hr.Seek(1000, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 50)
	n, err := hr.Read(buf)
	if n != 50 || err != nil {
		t.Fatalf("Read after Seek = %d, %v", n, err)
	}
	if !bytes.Equal(buf, body[1000:1050]) {
		t.Errorf("Read after Seek returned the wrong window")
	}
}

func TestReadEmptyFile(t *testing.T) {
	hr, err := NewHTTPReader(rangeServer(t, nil, nil).URL)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := hr.Read(make([]byte, 16)); n != 0 || err != io.EOF {
		t.Errorf("Read on an empty file = %d, %v; want 0, io.EOF", n, err)
	}
}
