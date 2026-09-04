package common

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/minio/minio-go/v7"
)

const (
	maxRetries = 5
)

var (
	// var for tests to be able to override
	retryBaseDelay = 200 * time.Millisecond
	retryMaxDelay  = 3 * time.Second
)

type RetryReader struct {
	ctx      context.Context
	get      func(offset int64) (io.ReadCloser, error)
	expected int64 // total bytes expected, -1 when unknown
	rc       io.ReadCloser
	offset   int64
	fails    int
}

// NewRetryReader streams the result of get(0) and in case of a transient
// failure during the read retries get(offset) until max retries or until
// expected||EOF
func NewRetryReader(ctx context.Context, expected int64, get func(offset int64) (io.ReadCloser, error)) *RetryReader {
	return &RetryReader{ctx: ctx, get: get, expected: expected}
}

func (r *RetryReader) Read(p []byte) (int, error) {
	for {
		// First run, or there was a failure, let's reopen it.
		if r.rc == nil {
			if r.expected >= 0 && r.offset >= r.expected {
				return 0, io.EOF
			}

			rc, err := r.get(r.offset)
			if err != nil {
				if r.pastEnd(err) {
					return 0, io.EOF
				}
				if err := r.backoff(err); err != nil {
					return 0, err
				}
				continue
			}
			r.rc = rc
		}

		n, err := r.rc.Read(p)
		r.offset += int64(n)

		if err == io.EOF && r.expected >= 0 && r.offset < r.expected {
			err = io.ErrUnexpectedEOF // short read, resume below
		}
		if err == nil || err == io.EOF {
			if n > 0 {
				r.fails = 0
			}
			return n, err
		}

		r.rc.Close()
		r.rc = nil

		if r.pastEnd(err) {
			return n, io.EOF
		}
		if n > 0 {
			// deliver what we have, resume on the next call
			r.fails = 0
			return n, nil
		}
		if err := r.backoff(err); err != nil {
			return 0, err
		}
	}
}

// pastEnd reports whether err is a 416 on a resumed read of an object of
// unknown size: the previous attempt failed right at the end of the
// object, so this is a completed read, not an error.
func (r *RetryReader) pastEnd(err error) bool {
	var resp minio.ErrorResponse
	return r.expected < 0 && r.offset > 0 &&
		errors.As(err, &resp) && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable
}

func (r *RetryReader) backoff(err error) error {
	if !retryable(err) {
		return err
	}
	if r.fails++; r.fails > maxRetries {
		return err
	}
	delay := min(retryBaseDelay<<(r.fails-1), retryMaxDelay)
	delay += rand.N(delay/2 + 1)
	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		switch resp.StatusCode {
		case 408, 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	// network-level errors: resets, timeouts, unexpected EOF, ...
	return true
}

func (r *RetryReader) Close() error {
	if r.rc == nil {
		return nil
	}
	return r.rc.Close()
}
