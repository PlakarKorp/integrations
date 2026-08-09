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

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
)

type Example struct {
	// used in the storage
	config    []byte
	packfiles sync.Map
	states    sync.Map
	locks     sync.Map
}

func init() {
	importer.Register("example", location.FLAG_LOCALFS, NewImporter)
	exporter.Register("example", location.FLAG_LOCALFS, NewExporter)
	storage.Register("example", location.FLAG_LOCALFS, NewStore)
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	return &Example{}, nil
}

func NewExporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	return &Example{}, nil
}

func NewStore(ctx context.Context, proto string, config map[string]string) (storage.Store, error) {
	return &Example{}, nil
}

func (e *Example) Root() string          { return "/" }
func (e *Example) Origin() string        { return "localhost" }
func (e *Example) Type() string          { return "example" }
func (e *Example) Flags() location.Flags { return 0 }

func (e *Example) Ping(ctx context.Context) error {
	return nil
}

func (e *Example) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	for i := range 5 {
		var (
			base     = fmt.Sprintf("file-%d", i)
			fullpath = fmt.Sprintf("/path/to/%s", base)
			content  = fmt.Sprintf("content of the file %d\n", i)
		)

		fi := objects.FileInfo{
			Lname:    base,
			Lsize:    int64(len(content)),
			Lmode:    0x644,
			LmodTime: time.Now(),
		}

		records <- connectors.NewRecord(fullpath, "", fi, nil, func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(content)), nil
		})
	}

	return nil
}

func (e *Example) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
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

func (e *Example) Mode(context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (e *Example) Create(ctx context.Context, config []byte) error {
	e.config = config
	return nil
}

func (e *Example) Open(ctx context.Context) ([]byte, error) {
	if e.config == nil {
		return nil, fmt.Errorf("store not created")
	}
	return e.config, nil
}

func (e *Example) Size(ctx context.Context) (int64, error) {
	// leave to plakar the job of figuring the actual size using
	// the states.  it's usually implemented only if there is an
	// easy way of getting the space used by the store, and only
	// by it.
	return -1, nil
}

func (e *Example) mapFor(res storage.StorageResource) (*sync.Map, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return &e.packfiles, nil
	case storage.StorageResourceState:
		return &e.states, nil
	case storage.StorageResourceLock:
		return &e.locks, nil
	}
	return nil, errors.ErrUnsupported
}

func (e *Example) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	m, err := e.mapFor(res)
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

func (e *Example) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	m, err := e.mapFor(res)
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

func (e *Example) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	m, err := e.mapFor(res)
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

func (e *Example) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	m, err := e.mapFor(res)
	if err != nil {
		return err
	}
	m.Delete(mac)
	return nil
}

func (e *Example) Close(ctx context.Context) error {
	return nil
}
