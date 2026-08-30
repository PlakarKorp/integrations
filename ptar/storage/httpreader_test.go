package storage

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var zeroTime = time.Time{}

var payload = []byte(strings.Repeat("0123456789abcdef", 64)) // 1024 bytes

// A well-behaved server: advertises ranges and answers 206.
func rangeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "ptar", zeroTime, bytes.NewReader(payload))
	}))
}

func TestHTTPReaderReadsCorrectWindows(t *testing.T) {
	srv := rangeServer(t)
	defer srv.Close()

	hr, err := NewHTTPReader(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if hr.size != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", hr.size, len(payload))
	}

	buf := make([]byte, 100)
	n, err := hr.ReadAt(buf, 200)
	if err != nil || n != 100 {
		t.Fatalf("ReadAt = %d, %v", n, err)
	}
	if !bytes.Equal(buf, payload[200:300]) {
		t.Errorf("ReadAt returned the wrong window")
	}

	got, err := io.ReadAll(hr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("sequential read returned %d bytes, want %d", len(got), len(payload))
	}
}

// A server that ignores Range answers 200 with the whole body.  Reading the
// head of that as though it were the requested window silently returns the
// wrong bytes, so it has to be refused.
func TestHTTPReaderRejectsIgnoredRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		w.Write(payload)
	}))
	defer srv.Close()

	hr, err := NewHTTPReader(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 10)
	if _, err := hr.ReadAt(buf, 500); err == nil {
		t.Fatal("a 200 answer to a Range request was accepted as the range")
	}
}

// Without Range support the reader cannot work at all; say so up front rather
// than returning wrong data later.
func TestNewHTTPReaderRequiresRangeSupport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Write(payload)
	}))
	defer srv.Close()

	if _, err := NewHTTPReader(srv.URL); err == nil {
		t.Fatal("a server without Accept-Ranges was accepted")
	}
}

func TestNewHTTPReaderSetsATimeout(t *testing.T) {
	srv := rangeServer(t)
	defer srv.Close()

	hr, err := NewHTTPReader(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if hr.client.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", hr.client.Timeout, defaultTimeout)
	}
}
