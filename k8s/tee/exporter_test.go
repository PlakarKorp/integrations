package tee

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/require"
)

type fakeExporter struct {
	export   func(context.Context, <-chan *connectors.Record, chan<- *connectors.Result) error
	closeErr error
	closed   atomic.Bool
}

func (f *fakeExporter) Origin() string             { return "test" }
func (f *fakeExporter) Type() string               { return "test" }
func (f *fakeExporter) Root() string               { return "/" }
func (f *fakeExporter) Flags() location.Flags      { return location.FLAG_STREAM | location.FLAG_NEEDACK }
func (f *fakeExporter) Ping(context.Context) error { return nil }

func (f *fakeExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	return f.export(ctx, records, results)
}

func (f *fakeExporter) Close(context.Context) error {
	f.closed.Store(true)
	return f.closeErr
}

// echo acks every record it receives and exits cleanly.
func echo(_ context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)
	for record := range records {
		results <- record.Ok()
	}
	return nil
}

func rec(pathname string) *connectors.Record {
	return connectors.NewRecord(pathname, "", objects.FileInfo{Lname: pathname}, nil,
		func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		})
}

// nohang runs fn and fails the test if it doesn't return in a certain
// time.  fn must not assert: capture and check after.
func nohang(t *testing.T, what string, fn func()) {
	t.Helper()

	ch := make(chan struct{})
	go func() {
		defer close(ch)
		fn()
	}()

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not return within 2s (deadlock?)", what)
	}
}

func TestPushReturnsResultForEachRecord(t *testing.T) {
	exp := &fakeExporter{export: echo}
	e := New(t.Context(), exp)

	for _, pathname := range []string{"/a", "/b"} {
		var (
			res *connectors.Result
			err error
		)
		nohang(t, "Push("+pathname+")", func() { res, err = e.Push(rec(pathname)) })

		require.NoError(t, err)
		require.NotNil(t, res)
		require.NoError(t, res.Err)
		require.Equal(t, pathname, res.Record.Pathname)
	}

	var err error
	nohang(t, "Wait", func() { err = e.Wait() })
	require.NoError(t, err)
	require.True(t, exp.closed.Load(), "Wait must close the exporter")
}

func TestWaitReportsExporterError(t *testing.T) {
	exp := &fakeExporter{export: func(_ context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
		defer close(results)
		for range records {
			// never ack
		}
		return errors.New("export boom")
	}}
	e := New(t.Context(), exp)

	var err error
	nohang(t, "Wait", func() { err = e.Wait() })
	require.ErrorContains(t, err, "export boom")
}

func TestWaitReportsCloseFailure(t *testing.T) {
	exp := &fakeExporter{export: echo, closeErr: errors.New("close boom")}
	e := New(t.Context(), exp)

	var err error
	nohang(t, "Wait", func() { err = e.Wait() })
	require.ErrorContains(t, err, "close boom")
}

func TestPushSurfacesFailedExporter(t *testing.T) {
	exp := &fakeExporter{export: func(_ context.Context, _ <-chan *connectors.Record, results chan<- *connectors.Result) error {
		close(results)
		return errors.New("boom")
	}}
	e := New(t.Context(), exp)

	var (
		res *connectors.Result
		err error
	)
	// nobody is reading records, so this can only return by noticing
	// that the exporter died.
	nohang(t, "Push", func() { res, err = e.Push(rec("/a")) })

	require.Nil(t, res)
	require.ErrorContains(t, err, "boom")
}

func TestPushAlwaysReportsResultOrError(t *testing.T) {
	// an exporter that takes a record and dies without acking it races
	// two outcomes inside Push: the closed results channel and the
	// error channel.  Whichever wins, a caller must never be handed
	// (nil, nil) -- it has no way to tell what happened.
	for i := range 20 {
		exp := &fakeExporter{export: func(_ context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
			defer close(results)
			<-records
			return errors.New("died mid-record")
		}}
		e := New(t.Context(), exp)

		var (
			res *connectors.Result
			err error
		)
		nohang(t, "Push", func() { res, err = e.Push(rec("/a")) })

		require.False(t, res == nil && err == nil,
			"iteration %d: Push returned no result and no error", i)
	}
}
