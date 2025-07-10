package fs

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/exporter"
)

type FSExporter struct {
	rootDir string
}

func NewFSExporter(ctx context.Context, opts *exporter.Options, name string, config map[string]string) (exporter.Exporter, error) {
	return &FSExporter{
		rootDir: strings.TrimPrefix(config["location"], "fis://"),
	}, nil
}

func (p *FSExporter) Root() string {
	return p.rootDir
}

func (p *FSExporter) CreateDirectory(pathname string) error {
	return os.MkdirAll(pathname, 0700)
}

func (p *FSExporter) StoreFile(pathname string, fp io.Reader, size int64) error {
	f, err := os.Create(pathname)
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, fp); err != nil {
		f.Close()
		return err
	}
	return nil
}

func (p *FSExporter) SetPermissions(pathname string, fileinfo *objects.FileInfo) error {
	if err := os.Chmod(pathname, fileinfo.Mode()); err != nil {
		return err
	}
	if os.Getuid() == 0 {
		if err := os.Chown(pathname, int(fileinfo.Uid()), int(fileinfo.Gid())); err != nil {
			return err
		}
	}
	return nil
}

func (p *FSExporter) Close() error {
	return nil
}
