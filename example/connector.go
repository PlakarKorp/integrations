package connector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
)

type testConnector struct{}

type testStore struct {
	config    []byte
	packfiles sync.Map
	states    sync.Map
	locks     sync.Map
}

const FILE = "/home/tracepanic/Documents/notes.md"

func init() {
	importer.Register("test", location.FLAG_LOCALFS, NewImporter)
	exporter.Register("test", location.FLAG_LOCALFS, NewExporter)
	storage.Register("test", location.FLAG_LOCALFS, NewStore)
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	return &testConnector{}, nil
}

func NewExporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	return &testConnector{}, nil
}

func NewStore(ctx context.Context, proto string, config map[string]string) (storage.Store, error) {
	return &testStore{}, nil
}

func (f *testConnector) Root() string   { return "/home/tracepanic/Documents" }
func (f *testConnector) Origin() string { return "localhost" }
func (f *testConnector) Type() string   { return "test" }

func (f *testConnector) Flags() location.Flags {
	return location.FLAG_LOCALFS
}

func (f *testConnector) Ping(ctx context.Context) error {
	return nil
}

func (f *testConnector) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	info, err := os.Stat(FILE)
	if err != nil {
		return err
	}

	fi := objects.FileInfo{
		Lname:    filepath.Base(FILE),
		Lsize:    info.Size(),
		Lmode:    info.Mode(),
		LmodTime: info.ModTime(),
		Ldev:     1,
	}

	records <- connectors.NewRecord(FILE, "", fi, nil, func() (io.ReadCloser, error) {
		return os.Open(FILE)
	})

	return nil
}

func (f *testConnector) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
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
func (s *testStore) Root() string          { return "memory" }
func (s *testStore) Type() string          { return "test" }
func (s *testStore) Flags() location.Flags { return location.FLAG_LOCALFS }

func (s *testStore) Ping(ctx context.Context) error {
	return nil
}

func (s *testStore) Mode(context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (s *testStore) Create(ctx context.Context, config []byte) error {
	s.config = config
	return nil
}

func (s *testStore) Open(ctx context.Context) ([]byte, error) {
	if s.config == nil {
		return nil, fmt.Errorf("store not created")
	}
	return s.config, nil
}

func (s *testStore) Size(ctx context.Context) (int64, error) {
	var size int64
	for _, m := range []*sync.Map{&s.packfiles, &s.states, &s.locks} {
		m.Range(func(_, value any) bool {
			size += int64(len(value.([]byte)))
			return true
		})
	}
	return size, nil
}

func (s *testStore) mapFor(res storage.StorageResource) (*sync.Map, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return &s.packfiles, nil
	case storage.StorageResourceState:
		return &s.states, nil
	case storage.StorageResourceLock:
		return &s.locks, nil
	}
	return nil, errors.ErrUnsupported
}

func (s *testStore) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	m, err := s.mapFor(res)
	if err != nil {
		return nil, err
	}
	var macs []objects.MAC
	m.Range(func(key, _ any) bool {
		macs = append(macs, key.(objects.MAC))
		return true
	})
	return macs, nil
}

func (s *testStore) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
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

func (s *testStore) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	m, err := s.mapFor(res)
	if err != nil {
		return nil, err
	}
	v, ok := m.Load(mac)
	if !ok {
		if res == storage.StorageResourcePackfile {
			return nil, repository.ErrPackfileNotFound
		}
		return nil, fmt.Errorf("not found")
	}
	data := v.([]byte)
	if rg != nil {
		end := min(rg.Offset+uint64(rg.Length), uint64(len(data)))
		data = data[rg.Offset:end]
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *testStore) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
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
