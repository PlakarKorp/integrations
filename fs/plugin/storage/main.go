package main

import (
	"context"
	"errors"
	"io"
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integrations/fs/storage"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
)

type failingStorage struct {
	storage.Store
}

var ErrWrongContext = errors.New("fs integration cannot be used in this context")

func (failingStorage) Create(context.Context, []byte) error { return ErrWrongContext }
func (failingStorage) Open(context.Context) ([]byte, error) { return nil, ErrWrongContext }

func (failingStorage) Mode(context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (failingStorage) Size(context.Context) (int64, error) { return -1, nil }

func (failingStorage) List(context.Context, storage.StorageResource) ([]objects.MAC, error) {
	return nil, ErrWrongContext
}

func (failingStorage) Put(context.Context, storage.StorageResource, objects.MAC, io.Reader) (int64, error) {
	return -1, ErrWrongContext
}

func (failingStorage) Get(context.Context, storage.StorageResource, objects.MAC, *storage.Range) (io.ReadCloser, error) {
	return nil, ErrWrongContext
}

func (failingStorage) Delete(context.Context, storage.StorageResource, objects.MAC) error {
	return ErrWrongContext
}

func failer(ctx context.Context, name string, config map[string]string) (storage.Store, error) {
	i, err := fs.NewStore(ctx, name, config)
	if err != nil {
		return nil, err
	}
	return failingStorage{i}, nil
}

func main() {
	sdk.EntrypointStorage(os.Args, failer)
}
