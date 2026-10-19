package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	rsyncconfig "github.com/PlakarKorp/integrations/rsync/internal/config"
	"github.com/PlakarKorp/integrations/rsync/internal/rsync"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

func init() {
	importer.Register("rsync", 0, NewImporter)
}

type Importer struct {
	// Content is fetched after Import returns, so neither the context given
	// to NewImporter (the plugin's Init call) nor Import's can bound it.
	ctx    context.Context
	cancel context.CancelFunc
	client *rsync.Client
	host   string
	dir    string // relative to the module root
	root   string
}

func NewImporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	cfg, err := rsyncconfig.Parse(config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Importer{
		ctx:    ctx,
		cancel: cancel,
		client: cfg.NewClient(),
		host:   cfg.Host,
		dir:    cfg.Dir,
		root:   cfg.Root,
	}, nil
}

func (imp *Importer) Origin() string        { return imp.host }
func (imp *Importer) Type() string          { return "rsync" }
func (imp *Importer) Root() string          { return imp.root }
func (imp *Importer) Flags() location.Flags { return 0 }

func (imp *Importer) Ping(ctx context.Context) error {
	return imp.client.Ping(ctx, imp.dir)
}

func (imp *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	files, errs, err := imp.client.List(ctx, imp.dir)
	if err != nil {
		return err
	}
	// The server does not say which path an error is about.
	for _, msg := range errs {
		records <- connectors.NewError(imp.root, errors.New(msg))
	}

	for _, f := range files {
		pathname := path.Join(imp.root, f.Name)
		fileinfo := objects.FileInfo{
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
			name := path.Join(imp.dir, f.Name)
			records <- connectors.NewRecord(pathname, "", fileinfo, nil, func() (io.ReadCloser, error) {
				return imp.client.Open(imp.ctx, name)
			})
		case fs.ModeDir, fs.ModeSymlink:
			records <- connectors.NewRecord(pathname, f.LinkTarget, fileinfo, nil, nil)
		default:
			records <- connectors.NewError(pathname, fmt.Errorf("unsupported file type %s", f.Mode.Type()))
		}
	}
	return nil
}

func (imp *Importer) Close(ctx context.Context) error {
	imp.cancel()
	return nil
}
