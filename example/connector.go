package connector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
)

type testConnector struct{}

type testStore struct {
	config    []byte
	packfiles sync.Map
	states    sync.Map
	locks     sync.Map
}

// ErrNotFound is returned by Get for a missing object.
var ErrNotFound = errors.New("not found")

func NewImporter(ctx context.Context, opts *integration.Options, proto string, config map[string]string) (integration.Importer, error) {
	return &testConnector{}, nil
}

func NewExporter(ctx context.Context, opts *integration.Options, proto string, config map[string]string) (integration.Exporter, error) {
	return &testConnector{}, nil
}

func NewStore(ctx context.Context, proto string, config map[string]string) (integration.Store, error) {
	return &testStore{}, nil
}

func (f *testConnector) Root() string          { return "/" }
func (f *testConnector) Origin() string        { return "localhost" }
func (f *testConnector) Type() string          { return "example" }
func (f *testConnector) Flags() location.Flags { return location.FLAG_LOCALFS }

func (f *testConnector) Ping(ctx context.Context) error {
	return nil
}

func (f *testConnector) Import(ctx context.Context, records chan<- *integration.Record, results <-chan *integration.Result) error {
	defer close(records)

	for i := range 5 {
		var (
			base     = fmt.Sprintf("file-%d", i)
			fullpath = "/path/to/" + base
			content  = fmt.Sprintf("content of the file %d\n", i)
		)

		fi := integration.FileInfo{
			Lname:    base,
			Lsize:    int64(len(content)),
			Lmode:    0x644,
			LmodTime: time.Now(),
		}

		records <- integration.NewRecord(fullpath, "", fi, nil, func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(content)), nil
		})
	}

	return nil
}

func (f *testConnector) Export(ctx context.Context, records <-chan *integration.Record, results chan<- *integration.Result) error {
	defer close(results)

	for record := range records {
		fmt.Fprintf(os.Stderr, "--- %s ---\n", record.Pathname)

		if record.Reader != nil {
			if _, err := io.Copy(os.Stderr, record.Reader); err != nil {
				results <- record.Error(err)
				continue
			}
			fmt.Fprintln(os.Stderr)
		}

		results <- record.Ok()
	}

	return nil
}

func (f *testConnector) Close(ctx context.Context) error {
	return nil
}

// Storage connector methods

func (s *testStore) Origin() string        { return "localhost" }
func (s *testStore) Root() string          { return "/" }
func (s *testStore) Type() string          { return "example" }
func (s *testStore) Flags() location.Flags { return location.FLAG_LOCALFS }

func (s *testStore) Ping(ctx context.Context) error {
	return nil
}

func (s *testStore) Mode(context.Context) (integration.Mode, error) {
	return integration.ModeRead | integration.ModeWrite, nil
}

func (s *testStore) Create(ctx context.Context, config []byte) error {
	s.config = config
	return nil
}

func (s *testStore) Open(ctx context.Context) ([]byte, error) {
	if s.config == nil {
		return nil, errors.New("store not created")
	}
	return s.config, nil
}

func (s *testStore) Size(ctx context.Context) (int64, error) {
	// leave to plakar the job of figuring the actual size using
	// the states.  it's usually implemented only if there is an
	// easy way of getting the space used by the store, and only
	// by it.
	return -1, nil
}

func (s *testStore) mapFor(res integration.StorageResource) (*sync.Map, error) {
	switch res {
	case integration.StorageResourcePackfile:
		return &s.packfiles, nil
	case integration.StorageResourceState:
		return &s.states, nil
	case integration.StorageResourceLock:
		return &s.locks, nil
	default:
		return nil, errors.ErrUnsupported
	}
}

func (s *testStore) List(ctx context.Context, res integration.StorageResource) ([]integration.MAC, error) {
	m, err := s.mapFor(res)
	if err != nil {
		return nil, err
	}
	var macs []integration.MAC
	m.Range(func(key, _ any) bool {
		macs = append(macs, key.(integration.MAC))
		return true
	})
	return macs, nil
}

func (s *testStore) Put(ctx context.Context, res integration.StorageResource, mac integration.MAC, rd io.Reader) (int64, error) {
	m, err := s.mapFor(res)
	if err != nil {
		return -1, err
	}
	data, err := io.ReadAll(rd)
	if err != nil {
		return -1, err
	}
	m.Store(mac, data)
	return int64(len(data)), nil
}

func (s *testStore) Get(ctx context.Context, res integration.StorageResource, mac integration.MAC, rg *integration.Range) (io.ReadCloser, error) {
	m, err := s.mapFor(res)
	if err != nil {
		return nil, err
	}
	v, ok := m.Load(mac)
	if !ok {
		return nil, ErrNotFound
	}
	data := v.([]byte)
	if rg != nil {
		end := min(rg.Offset+uint64(rg.Length), uint64(len(data)))
		data = data[rg.Offset:end]
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *testStore) Delete(ctx context.Context, res integration.StorageResource, mac integration.MAC) error {
	m, err := s.mapFor(res)
	if err != nil {
		return err
	}
	m.Delete(mac)
	return nil
}

func (s *testStore) Close(ctx context.Context) error {
	return nil
}
