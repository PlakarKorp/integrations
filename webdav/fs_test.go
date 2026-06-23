package webdav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sync"
	"time"

	"github.com/emersion/go-webdav"
)

type item struct {
	name    string
	dir     bool
	content string
}

// testFS implements a webdav FileSystem for testing purposes.
type testFS struct {
	prefix  string
	backing fs.ReadDirFS

	overlay map[string]item
	mtx     sync.Mutex
}

// type-check
var _ webdav.FileSystem = &testFS{}

func (tfs *testFS) mangle(p string) string { return path.Join(tfs.prefix, p) }

func (tfs *testFS) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	return tfs.backing.Open(tfs.mangle(name))
}

func (tfs *testFS) statToFileInfo(name string, sb fs.FileInfo) *webdav.FileInfo {
	return &webdav.FileInfo{
		Path:     name,
		Size:     sb.Size(),
		ModTime:  sb.ModTime(),
		IsDir:    sb.IsDir(),
		MIMEType: "application/octet-stream",
	}
}

func (tfs *testFS) Stat(ctx context.Context, name string) (*webdav.FileInfo, error) {
	fp, err := tfs.backing.Open(tfs.mangle(name))
	if err != nil {
		return nil, err
	}
	defer fp.Close()

	sb, err := fp.Stat()
	if err != nil {
		return nil, err
	}

	return tfs.statToFileInfo(name, sb), nil
}

func (tfs *testFS) ReadDir(ctx context.Context, name string, recursive bool) (fis []webdav.FileInfo, err error) {
	dirents, err := tfs.backing.ReadDir(tfs.mangle(name))
	if err != nil {
		return nil, err
	}

	// at least nextcloud seem to return the dir itself in the readdir result
	fis = append(fis, webdav.FileInfo{
		Path:    name,
		Size:    0,
		ModTime: time.Now(),
		IsDir:   true,
	})

	for _, dirent := range dirents {
		sb, err := dirent.Info()
		if err != nil {
			return nil, err
		}

		p := path.Join(name, sb.Name())
		fis = append(fis, *tfs.statToFileInfo(p, sb))
	}
	return
}

func (tfs *testFS) Create(ctx context.Context, name string, body io.ReadCloser, opts *webdav.CreateOptions) (fileInfo *webdav.FileInfo, created bool, err error) {
	tfs.mtx.Lock()
	defer tfs.mtx.Unlock()

	defer body.Close()

	if _, ok := tfs.overlay[name]; ok {
		return nil, false, fmt.Errorf("%s already exists", name)
	}

	buf, err := io.ReadAll(body)
	if err != nil {
		return nil, false, err
	}

	tfs.overlay[name] = item{
		name:    name,
		content: string(buf),
	}

	return &webdav.FileInfo{
		Path:     name,
		Size:     int64(len(buf)),
		ModTime:  time.Now(),
		IsDir:    false,
		MIMEType: "application/octet-stream",
	}, true, nil
}

func (tfs *testFS) Mkdir(ctx context.Context, name string) error {
	tfs.mtx.Lock()
	defer tfs.mtx.Unlock()

	if _, ok := tfs.overlay[name]; ok {
		return fmt.Errorf("%s already exists", name)
	}

	tfs.overlay[name] = item{
		name: name,
		dir:  true,
	}

	return nil
}

// these methods are not needed by Export()

func (tfs *testFS) RemoveAll(ctx context.Context, name string, opts *webdav.RemoveAllOptions) error {
	return errors.ErrUnsupported
}

func (tfs *testFS) Copy(ctx context.Context, name, dest string, options *webdav.CopyOptions) (created bool, err error) {
	return false, errors.ErrUnsupported
}

func (tfs *testFS) Move(ctx context.Context, name, dest string, options *webdav.MoveOptions) (created bool, err error) {
	return false, errors.ErrUnsupported
}
