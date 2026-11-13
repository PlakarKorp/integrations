package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
	rsyncconfig "github.com/PlakarKorp/integrations/rsync/internal/config"
	"github.com/PlakarKorp/integrations/rsync/internal/rsync"
)

func init() {
	_ = integration.RegisterImporter("rsync", 0, NewImporter)
}

type Importer struct {
	// Content is fetched after Import returns, so neither the context given
	// to NewImporter (the plugin's Init call) nor Import's can bound it.
	ctx    context.Context
	cancel context.CancelFunc
	cfg    *rsyncconfig.Config
	client *rsync.Client
}

func NewImporter(appCtx context.Context, opts *integration.Options, name string, config map[string]string) (integration.Importer, error) {
	cfg, err := rsyncconfig.Parse(config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Importer{
		ctx:    ctx,
		cancel: cancel,
		cfg:    cfg,
		client: cfg.NewClient(),
	}, nil
}

func (imp *Importer) Origin() string        { return imp.cfg.Host }
func (imp *Importer) Type() string          { return "rsync" }
func (imp *Importer) Root() string          { return imp.cfg.Root() }
func (imp *Importer) Flags() location.Flags { return 0 }

func (imp *Importer) Ping(ctx context.Context) error {
	return imp.client.Ping(ctx, imp.cfg.Dir)
}

func (imp *Importer) Import(ctx context.Context, records chan<- *integration.Record, results <-chan *integration.Result) (map[string]string, error) {
	defer close(records)

	files, errs, err := imp.client.List(ctx, imp.cfg.Dir)
	if err != nil {
		return nil, err
	}
	// The server does not say which path an error is about.
	for _, msg := range errs {
		records <- integration.NewError(imp.cfg.Root(), errors.New(msg))
	}

	for _, f := range files {
		pathname := path.Join(imp.cfg.Root(), f.Name)
		fileinfo := integration.FileInfo{
			Lname:      path.Base(pathname),
			Lsize:      f.Size,
			Lmode:      f.Mode,
			LmodTime:   f.ModTime,
			Luid:       uint64(f.UID),
			Lgid:       uint64(f.GID),
			Lusername:  f.User,
			Lgroupname: f.Group,
		}

		switch f.Mode.Type() {
		case 0:
			name := path.Join(imp.cfg.Dir, f.Name)
			records <- integration.NewRecord(pathname, "", fileinfo, nil, func() (io.ReadCloser, error) {
				return imp.client.Open(imp.ctx, name)
			})
		case fs.ModeDir, fs.ModeSymlink:
			records <- integration.NewRecord(pathname, f.LinkTarget, fileinfo, nil, nil)
		default:
			records <- integration.NewError(pathname, fmt.Errorf("unsupported file type %s", f.Mode.Type()))
		}
	}
	return nil, nil //nolint:nilnil // no import metadata
}

func (imp *Importer) Close(ctx context.Context) error {
	imp.cancel()
	return nil
}
