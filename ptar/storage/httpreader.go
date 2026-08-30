package storage

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type ReadWriteSeekStatReadAtCloser interface {
	io.Reader   // Read(p []byte) (n int, err error)
	io.Seeker   // Seek(offset int64, whence int) (int64, error)
	io.Closer   // Close() error
	io.ReaderAt // ReadAt(p []byte, off int64) (n int, err error)
	io.Writer   // Write(p []byte) (n int, err error)
	Stat() (os.FileInfo, error)
}

type HTTPReader struct {
	client *http.Client
	url    string
	offset int64
	size   int64
}

// defaultTimeout bounds a whole request.  http.Head and a zero-value
// http.Client have no timeout at all, so an unresponsive server hung the
// caller with nothing to cancel.
const defaultTimeout = 5 * time.Minute

func NewHTTPReader(url string) (*HTTPReader, error) {
	client := &http.Client{Timeout: defaultTimeout}

	resp, err := client.Head(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("could not open ptar: %s", resp.Status)
	}

	contentLength, err := strconv.Atoi(resp.Header.Get("Content-Length"))
	if err != nil {
		return nil, fmt.Errorf("could not determine ptar size: %w", err)
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("negative Content-Length %d", contentLength)
	}

	// Range requests are what every read below depends on.  A server that
	// ignores them answers 200 with the whole body, which would then be
	// treated as though it were the requested window.
	if !strings.Contains(strings.ToLower(resp.Header.Get("Accept-Ranges")), "bytes") {
		return nil, fmt.Errorf("server does not advertise byte ranges for %s; "+
			"a ptar over HTTP needs Range support", url)
	}

	hr := HTTPReader{
		client: client,
		url:    url,
		offset: 0,
		size:   int64(contentLength),
	}
	return &hr, nil
}

// rangeRequest issues a GET for [off, end] and returns the body, having
// checked that the server actually honoured the range.
func (hr *HTTPReader) rangeRequest(off, end int64) (*http.Response, error) {
	req, err := http.NewRequest("GET", hr.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))

	resp, err := hr.client.Do(req)
	if err != nil {
		return nil, err
	}

	// 206 is the only answer that means "here is the window you asked for".
	// A 200 is the whole file, and reading the head of it as though it were
	// the range silently returns the wrong bytes.
	if resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil, fmt.Errorf("server ignored the Range header and returned the whole body")
		}
		return nil, fmt.Errorf("HTTP status %s", resp.Status)
	}

	return resp, nil
}

func (hr *HTTPReader) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	if hr.offset >= hr.size {
		return 0, io.EOF
	}

	end := hr.offset + int64(len(buf)) - 1
	if end >= hr.size {
		end = hr.size - 1
	}

	resp, err := hr.rangeRequest(hr.offset, end)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	// A single Body.Read returned whatever the first packet happened to
	// carry and reported it as the whole read.  Fill the window.
	n, err := io.ReadFull(resp.Body, buf[:end-hr.offset+1])
	hr.offset += int64(n)
	if err == io.ErrUnexpectedEOF {
		err = nil
	}
	return n, err
}

func (hr *HTTPReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		if offset >= hr.size {
			return 0, io.EOF
		}
		hr.offset = offset
	case io.SeekCurrent:
		if hr.offset+offset >= hr.size {
			return 0, io.EOF
		}
		hr.offset += offset
	case io.SeekEnd:
		if offset > hr.size {
			return 0, io.EOF
		}
		hr.offset = hr.size + offset
	}
	return hr.offset, nil
}

func (hr *HTTPReader) ReadAt(buf []byte, off int64) (int, error) {
	if off >= hr.size {
		return 0, io.EOF
	}

	end := off + int64(len(buf)) - 1
	if end >= hr.size {
		end = hr.size - 1
	}

	resp, err := hr.rangeRequest(off, end)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	n, err := io.ReadFull(resp.Body, buf[:end-off+1])
	if err != nil && err != io.ErrUnexpectedEOF {
		return n, err
	}
	return n, nil
}

type dummyFileInfo struct {
	name string
	size int64
}

func (d *dummyFileInfo) Name() string       { return "http_reader" }
func (d *dummyFileInfo) Size() int64        { return d.size }
func (d *dummyFileInfo) Mode() os.FileMode  { return 0644 }
func (d *dummyFileInfo) ModTime() time.Time { return time.Time{} }
func (d *dummyFileInfo) IsDir() bool        { return false }
func (d *dummyFileInfo) Sys() interface{}   { return nil }

func (hr *HTTPReader) Stat() (os.FileInfo, error) {
	// Since HTTP does not provide file info, we can return a dummy FileInfo
	return &dummyFileInfo{
		size: hr.size,
	}, nil
}

func (hr *HTTPReader) Write(p []byte) (int, error) {
	return 0, fmt.Errorf("HTTPReader does not support Write")
}

func (hr *HTTPReader) Close() error {
	return nil
}
