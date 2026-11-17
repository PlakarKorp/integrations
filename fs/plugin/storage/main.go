package main

import (
	"context"
	"errors"
	"io"
	"iter"
	"os"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/server"
	fs "github.com/PlakarKorp/integrations/fs/storage"
)

type failingStorage struct {
	integration.Store
}

var ErrWrongContext = errors.New("fs integration cannot be used in this context")

func (failingStorage) Create(context.Context, []byte) error { return ErrWrongContext }
func (failingStorage) Open(context.Context) ([]byte, error) { return nil, ErrWrongContext }

func (failingStorage) Mode(context.Context) (integration.Mode, error) {
	return integration.ModeRead | integration.ModeWrite, nil
}

func (failingStorage) Size(context.Context) (int64, error) { return -1, nil }

func (failingStorage) List(context.Context, integration.StorageResource, integration.StorageFlag) iter.Seq2[*integration.StorageResourceEntry, error] {
	return func(yield func(*integration.StorageResourceEntry, error) bool) {
		yield(nil, ErrWrongContext)
	}
}

func (failingStorage) Put(context.Context, integration.StorageResource, integration.StorageFlag, integration.MAC, io.Reader) (int64, error) {
	return -1, ErrWrongContext
}

func (failingStorage) Get(context.Context, integration.StorageResource, integration.StorageFlag, integration.MAC, *integration.Range) (io.ReadCloser, error) {
	return nil, ErrWrongContext
}

func (failingStorage) Delete(context.Context, integration.StorageResource, integration.StorageFlag, integration.MAC) error {
	return ErrWrongContext
}

func failer(ctx context.Context, name string, config map[string]string) (integration.Store, error) {
	i, err := fs.NewStore(ctx, name, config)
	if err != nil {
		return nil, err
	}
	return failingStorage{i}, nil
}

func main() {
	server.EntrypointStorage(failer, os.Args)
}
