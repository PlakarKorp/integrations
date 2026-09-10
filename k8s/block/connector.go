package block

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

const diskpath = "/disk.img"

type Block struct {
	hostname string
	device   string
}

func New(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (*Block, error) {
	device := strings.TrimPrefix(config["location"], proto+"://")
	if device == "" {
		return nil, fmt.Errorf("missing device path in location %q",
			config["location"])
	}

	return &Block{
		device:   device,
		hostname: opts.Hostname,
	}, nil
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	return New(ctx, opts, proto, config)
}

func NewExporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	return New(ctx, opts, proto, config)
}

func (b *Block) Origin() string                  { return b.hostname }
func (b *Block) Type() string                    { return "block" }
func (b *Block) Root() string                    { return "/" }
func (b *Block) Flags() location.Flags           { return 0 }
func (b *Block) Ping(ctx context.Context) error  { _, err := os.Stat(b.device); return err }
func (b *Block) Close(ctx context.Context) error { return nil }

// filesize computes the size of a device by seeking rather than via
// ioctl(BLKGETSIZE64).  also, it's useful for testing since there
// plain files are used.
func filesize(fp *os.File) (int64, error) {
	size, err := fp.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = fp.Seek(0, io.SeekStart)
	return size, err
}

func (b *Block) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	fp, err := os.Open(b.device)
	if err != nil {
		return fmt.Errorf("failed to open %q: %w", b.device, err)
	}

	size, err := filesize(fp)
	if err != nil {
		fp.Close()
		return fmt.Errorf("failed to get %q size: %w", b.device, err)
	}

	fi := objects.FileInfo{
		Lname:    path.Base(diskpath),
		Lsize:    size,
		Lmode:    0600,
		LmodTime: time.Now(),
	}
	records <- connectors.NewRecord(diskpath, "", fi, nil, func() (io.ReadCloser, error) {
		return fp, nil
	})
	return nil
}

func (b *Block) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	var saw bool
	for record := range records {
		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		if record.Pathname != diskpath {
			err := fmt.Errorf("unexpected file %q", record.Pathname)
			results <- record.Error(err)
			return err
		}

		saw = true

		fp, err := os.OpenFile(b.device, os.O_WRONLY, 0)
		if err != nil {
			err = fmt.Errorf("failed to open %q: %w", b.device, err)
			results <- record.Error(err)
			return err
		}

		n, err := io.Copy(fp, record.Reader)
		if err != nil {
			err = fmt.Errorf("failed to write %q: %w", b.device, err)
			results <- record.Error(err)
			return err
		}

		if err := fp.Close(); err != nil {
			err = fmt.Errorf("failed to close %q: %w", b.device, err)
			results <- record.Error(err)
			return err
		}

		if n != record.FileInfo.Lsize {
			err = fmt.Errorf("short write: wrote %d of %d bytes",
				n, record.FileInfo.Lsize)
			results <- record.Error(err)
			return err
		}

		results <- record.Ok()
	}

	if !saw {
		return fmt.Errorf("no %s image, wrong snapshot selected?", diskpath)
	}
	return nil
}
