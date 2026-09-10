package k8s

import (
	"context"
	"errors"
	"io"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/require"
)

type fakeImporter struct {
	records []*connectors.Record
	err     error
	acked   atomic.Uint64
}

func (f *fakeImporter) Origin() string              { return "test" }
func (f *fakeImporter) Type() string                { return "test" }
func (f *fakeImporter) Root() string                { return "/" }
func (f *fakeImporter) Flags() location.Flags       { return location.FLAG_STREAM | location.FLAG_NEEDACK }
func (f *fakeImporter) Ping(context.Context) error  { return nil }
func (f *fakeImporter) Close(context.Context) error { return nil }

func (f *fakeImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	go func() {
		defer close(records)
		for _, record := range f.records {
			records <- record
		}
	}()

	for range results {
		f.acked.Add(1)
	}
	return f.err
}

func mkrecords(pathnames ...string) []*connectors.Record {
	out := make([]*connectors.Record, 0, len(pathnames))
	for _, pathname := range pathnames {
		out = append(out, connectors.NewRecord(pathname, "", objects.FileInfo{
			Lname: path.Base(pathname),
		}, nil, func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		}))
	}
	return out
}

// sink simulates kloset: it reads the records and acks all of them.
type sink struct {
	Records chan *connectors.Record
	Results chan *connectors.Result

	mu   sync.Mutex
	seen []string

	stop    chan struct{}
	drained chan struct{}
	once    sync.Once
}

func newSink(t *testing.T, size int) *sink {
	s := &sink{
		Records: make(chan *connectors.Record, size),
		Results: make(chan *connectors.Result, size),
		stop:    make(chan struct{}),
		drained: make(chan struct{}),
	}

	go func() {
		defer close(s.drained)
		for {
			select {
			case <-s.stop:
				return
			case record := <-s.Records:
				s.mu.Lock()
				s.seen = append(s.seen, record.Pathname)
				s.mu.Unlock()
				s.Results <- record.Ok()
			}
		}
	}()

	t.Cleanup(s.close)
	return s
}

func (s *sink) close() {
	s.once.Do(func() {
		close(s.stop)
		<-s.drained
	})
}

func (s *sink) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

func TestFilter(t *testing.T) {
	keep := func(record *connectors.Record) *connectors.Record { return record }

	t.Run("forwards the records the callback keeps", func(t *testing.T) {
		imp := &fakeImporter{records: mkrecords("/a", "/b")}
		s := newSink(t, 4)

		err := filter(t.Context(), imp, s.Records, s.Results, func(record *connectors.Record) *connectors.Record {
			out := *record
			out.Pathname = "/disk" + record.Pathname
			return &out
		})
		require.NoError(t, err)

		require.Equal(t, []string{"/disk/a", "/disk/b"}, s.paths())
		require.EqualValues(t, 2, imp.acked.Load())
	})

	t.Run("acks the records the callback drops without forwarding them", func(t *testing.T) {
		imp := &fakeImporter{records: mkrecords("/", "/a")}
		s := newSink(t, 4)

		err := filter(t.Context(), imp, s.Records, s.Results, func(record *connectors.Record) *connectors.Record {
			if record.Pathname == "/" {
				return nil
			}
			return record
		})
		require.NoError(t, err)

		// only /a reached kloset, but the importer saw both
		require.Equal(t, []string{"/a"}, s.paths())
		require.EqualValues(t, 2, imp.acked.Load())
	})

	t.Run("yields the importer's error", func(t *testing.T) {
		imp := &fakeImporter{records: mkrecords("/a"), err: errors.New("boom")}
		s := newSink(t, 4)

		require.ErrorContains(t, filter(t.Context(), imp, s.Records, s.Results, keep), "boom")
	})

	t.Run("leaves the shared channels alone once it returns", func(t *testing.T) {
		s := newSink(t, 2)

		for _, disk := range []string{"/disk1", "/disk2", "/disk3"} {
			imp := &fakeImporter{records: mkrecords("/", "/disk.img")}

			err := filter(t.Context(), imp, s.Records, s.Results, func(record *connectors.Record) *connectors.Record {
				out := *record
				out.Pathname = path.Join(disk, record.Pathname)
				return &out
			})
			require.NoError(t, err, "disk %s", disk)
			require.EqualValues(t, 2, imp.acked.Load(), "disk %s", disk)
		}

		require.Equal(t, []string{
			"/disk1", "/disk1/disk.img",
			"/disk2", "/disk2/disk.img",
			"/disk3", "/disk3/disk.img",
		}, s.paths())
	})

	t.Run("does not leak its drain goroutine", func(t *testing.T) {
		s := newSink(t, 2)

		before := runtime.NumGoroutine()
		for range 3 {
			imp := &fakeImporter{records: mkrecords("/a", "/b")}
			require.NoError(t, filter(t.Context(), imp, s.Records, s.Results, keep))
		}
		s.close()

		require.Eventually(t, func() bool {
			return runtime.NumGoroutine() <= before
		}, 10*time.Second, 20*time.Millisecond,
			"a drain goroutine is still running after filter() returned")
	})
}
