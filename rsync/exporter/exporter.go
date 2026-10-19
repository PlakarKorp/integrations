package exporter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	rsyncconfig "github.com/PlakarKorp/integrations/rsync/internal/config"
	"github.com/PlakarKorp/integrations/rsync/internal/rsync"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"golang.org/x/sync/errgroup"
)

func init() {
	exporter.Register("rsync", 0, NewExporter)
}

type Exporter struct {
	cfg    *rsyncconfig.Config
	client *rsync.Client
}

func NewExporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	cfg, err := rsyncconfig.Parse(config)
	if err != nil {
		return nil, err
	}
	return &Exporter{cfg: cfg, client: cfg.NewClient()}, nil
}

func (exp *Exporter) Origin() string        { return exp.cfg.Host }
func (exp *Exporter) Type() string          { return "rsync" }
func (exp *Exporter) Root() string          { return exp.cfg.Root() }
func (exp *Exporter) Flags() location.Flags { return 0 }

func (exp *Exporter) Ping(ctx context.Context) error {
	return exp.client.Ping(ctx, "")
}

func (exp *Exporter) Close(ctx context.Context) error {
	return nil
}

// Export sends each record on its own connection: rsync needs the whole file
// list before any content, while records and their content arrive one by one.
func (exp *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	if err := exp.mkdirAll(ctx); err != nil {
		for record := range records {
			results <- record.Error(err)
		}
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(exp.cfg.MaxConns)

	// Writing into a directory changes its time: directories get their
	// metadata again once everything is written.
	var dirs []rsync.File

	for record := range records {
		if record.Err != nil || record.IsXattr {
			results <- record.Ok()
			continue
		}

		name := strings.TrimPrefix(path.Clean("/"+record.Pathname), "/")
		if name == "" {
			name = "."
		}
		// rsync refuses a name holding a "/" whose parent is not in the
		// same list: send each file into its parent.
		parent := path.Join(exp.cfg.Dir, path.Dir(name))
		f := toFile(path.Base(name), record)

		switch record.FileInfo.Lmode.Type() {
		case fs.ModeDir:
			// Created before the next record, which may be inside, and
			// writable until the final pass sets its mode.
			created := f
			created.Mode |= 0o700
			results <- result(record, exp.client.Send(ctx, parent, []rsync.File{created}, nil))
			f.Name = name
			dirs = append(dirs, f)
		case fs.ModeSymlink:
			g.Go(func() error {
				results <- result(record, exp.client.Send(gctx, parent, []rsync.File{f}, nil))
				return nil
			})
		case 0:
			g.Go(func() error {
				err := exp.client.Send(gctx, parent, []rsync.File{f}, func(rsync.File) (io.Reader, error) {
					return record.Reader, nil
				})
				results <- result(record, err)
				return nil
			})
		default:
			results <- record.Error(fmt.Errorf("unsupported file type %s", record.FileInfo.Lmode.Type()))
		}
	}
	_ = g.Wait()

	if len(dirs) == 0 {
		return ctx.Err()
	}
	return exp.client.Send(ctx, exp.cfg.Dir, dirs, nil)
}

// mkdirAll creates the missing directories of the destination path.
func (exp *Exporter) mkdirAll(ctx context.Context) error {
	if err := exp.client.Ping(ctx, exp.cfg.Dir); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	parts := strings.Split(exp.cfg.Dir, "/")
	for i := range parts {
		dir := path.Join(parts[:i+1]...)
		err := exp.client.Ping(ctx, dir)
		if err == nil {
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		f := rsync.File{Name: parts[i], Mode: fs.ModeDir | 0o755, ModTime: time.Now()}
		if err := exp.client.Send(ctx, path.Join(parts[:i]...), []rsync.File{f}, nil); err != nil {
			return err
		}
	}
	return nil
}

func toFile(name string, record *connectors.Record) rsync.File {
	fi := record.FileInfo
	return rsync.File{
		Name:       name,
		Size:       fi.Lsize,
		ModTime:    fi.LmodTime,
		Mode:       fi.Lmode,
		UID:        uint32(fi.Luid),
		GID:        uint32(fi.Lgid),
		User:       fi.Lusername,
		Group:      fi.Lgroupname,
		LinkTarget: record.Target,
	}
}

func result(record *connectors.Record, err error) *connectors.Result {
	if err != nil {
		return record.Error(err)
	}
	return record.Ok()
}
