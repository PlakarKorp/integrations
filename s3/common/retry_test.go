package common

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func init() { retryBaseDelay = time.Millisecond }

// attempt describes how one get() call behaves: fail to open, or serve
// up to `serve` bytes (-1 = all remaining) then return readErr (nil =
// clean EOF).
type attempt struct {
	openErr error
	serve   int
	readErr error
}

type errAfter struct {
	r   io.Reader
	err error
}

func (e *errAfter) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF && e.err != nil {
		return n, e.err
	}
	return n, err
}

func scripted(t *testing.T, data []byte, attempts []attempt) (func(int64) (io.ReadCloser, error), *[]int64) {
	offsets := &[]int64{}
	return func(off int64) (io.ReadCloser, error) {
		if len(*offsets) >= len(attempts) {
			t.Fatalf("unexpected attempt at offset %d", off)
		}
		a := attempts[len(*offsets)]
		*offsets = append(*offsets, off)
		if a.openErr != nil {
			return nil, a.openErr
		}
		end := len(data)
		if a.serve >= 0 {
			end = min(int(off)+a.serve, len(data))
		}
		return io.NopCloser(&errAfter{bytes.NewReader(data[off:end]), a.readErr}), nil
	}, offsets
}

func testData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i)
	}
	return data
}

var errReset = syscall.ECONNRESET

func s3err(status int) error {
	return minio.ErrorResponse{StatusCode: status}
}

func checkRead(t *testing.T, r io.Reader, want []byte) {
	t.Helper()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %d bytes, want %d", len(got), len(want))
	}
}

func checkOffsets(t *testing.T, got *[]int64, want ...int64) {
	t.Helper()
	if len(*got) != len(want) {
		t.Fatalf("got attempts at %v, want %v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Fatalf("got attempts at %v, want %v", *got, want)
		}
	}
}

func TestResumesAfterMidStreamError(t *testing.T) {
	data := testData(1000)
	get, offsets := scripted(t, data, []attempt{
		{serve: 300, readErr: errReset},
		{serve: -1},
	})
	checkRead(t, NewRetryReader(t.Context(), 1000, get), data)
	checkOffsets(t, offsets, 0, 300)
}

func TestShortReadWithKnownSizeResumes(t *testing.T) {
	data := testData(1000)
	get, offsets := scripted(t, data, []attempt{
		{serve: 300}, // clean EOF before expected size
		{serve: -1},
	})
	checkRead(t, NewRetryReader(t.Context(), 1000, get), data)
	checkOffsets(t, offsets, 0, 300)
}

func TestUnknownSizeEOFIsFinal(t *testing.T) {
	data := testData(1000)
	get, offsets := scripted(t, data, []attempt{
		{serve: 300},
	})
	checkRead(t, NewRetryReader(t.Context(), -1, get), data[:300])
	checkOffsets(t, offsets, 0)
}

func TestPermanentErrorNotRetried(t *testing.T) {
	get, offsets := scripted(t, nil, []attempt{
		{openErr: s3err(http.StatusNotFound)},
	})
	_, err := io.ReadAll(NewRetryReader(t.Context(), 10, get))
	var resp minio.ErrorResponse
	if !errors.As(err, &resp) || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %v", err)
	}
	checkOffsets(t, offsets, 0)
}

func TestTransientOpenErrorRetried(t *testing.T) {
	data := testData(100)
	get, offsets := scripted(t, data, []attempt{
		{openErr: s3err(http.StatusServiceUnavailable)},
		{serve: -1},
	})
	checkRead(t, NewRetryReader(t.Context(), 100, get), data)
	checkOffsets(t, offsets, 0, 0)
}

func TestRetryBudgetExhausted(t *testing.T) {
	attempts := make([]attempt, maxRetries+1)
	for i := range attempts {
		attempts[i] = attempt{openErr: s3err(http.StatusServiceUnavailable)}
	}
	get, offsets := scripted(t, nil, attempts)
	_, err := io.ReadAll(NewRetryReader(t.Context(), 10, get))
	if err == nil {
		t.Fatal("want error")
	}
	checkOffsets(t, offsets, 0, 0, 0, 0, 0, 0)
}

func TestProgressResetsBudget(t *testing.T) {
	// 8 mid-stream failures interleaved with progress: more total
	// failures than maxRetries, but never more than one in a row.
	data := testData(800)
	var attempts []attempt
	for range 8 {
		attempts = append(attempts, attempt{serve: 100, readErr: errReset})
	}
	get, _ := scripted(t, data, attempts)
	checkRead(t, NewRetryReader(t.Context(), 800, get), data)
}

func TestResumeAt416MeansEOF(t *testing.T) {
	data := testData(300)
	get, offsets := scripted(t, data, []attempt{
		{serve: -1, readErr: errReset}, // full body, then the reset
		{openErr: s3err(http.StatusRequestedRangeNotSatisfiable)},
	})
	checkRead(t, NewRetryReader(t.Context(), -1, get), data)
	checkOffsets(t, offsets, 0, 300)
}

func TestContextCancelStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	get, _ := scripted(t, nil, []attempt{
		{openErr: s3err(http.StatusServiceUnavailable)},
	})
	_, err := io.ReadAll(NewRetryReader(ctx, 10, get))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
