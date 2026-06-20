package rclone

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	rclonefs "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"golang.org/x/sync/errgroup"

	_ "github.com/rclone/rclone/backend/all" // register backends
)

type Rclone struct {
	typ         string
	base        string
	concurrency int
}

func init() {
	importer.Register("rclone", 0, NewImporter)
	exporter.Register("rclone", 0, NewExporter)
	storage.Register("rclone", 0, NewStorage)
}

func New(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (*Rclone, error) {
	location := params["location"]
	base := path.Clean("/" + strings.TrimPrefix(location, name+"://"))

	typ := params["rclone_type"]
	if typ == "" {
		return nil, fmt.Errorf("missing rclone_type")
	}

	rconfig := make(map[string]string, len(params)-1)
	for k, v := range params {
		if k, ok := strings.CutPrefix(k, "rclone_"); ok {
			rconfig[k] = v
		}
	}

	config.SetData(&mapconfig{name: typ, data: rconfig})

	return &Rclone{
		typ:         typ,
		base:        base,
		concurrency: opts.MaxConcurrency,
	}, nil
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (importer.Importer, error) {
	return New(ctx, opts, name, params)
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (exporter.Exporter, error) {
	return New(ctx, opts, name, params)
}

func NewStorage(ctx context.Context, name string, params map[string]string) (storage.Store, error) {
	return New(ctx, &connectors.Options{MaxConcurrency: 1}, name, params)
}

func (r *Rclone) Type() string          { return r.typ }
func (r *Rclone) Origin() string        { return "localhost" }
func (r *Rclone) Root() string          { return r.base }
func (r *Rclone) Flags() location.Flags { return 0 }

func (r *Rclone) openfs(ctx context.Context) (rclonefs.Fs, error) {
	return rclonefs.NewFs(ctx, fmt.Sprintf("%s:%s", r.typ, r.base))
}

func (r *Rclone) Ping(ctx context.Context) error {
	f, err := r.openfs(ctx)
	if err != nil {
		return err
	}
	_, err = f.List(ctx, "")
	return err
}

func (r *Rclone) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	f, err := r.openfs(ctx)
	if err != nil {
		return err
	}

	var wg errgroup.Group
	wg.SetLimit(r.concurrency)

	err = pwalk(ctx, f, &wg, "", func(p string, de rclonefs.DirEntry, err error) error {
		realpath := path.Join(r.base, p)

		if err != nil {
			err = fmt.Errorf("failed to walk %s: %w", realpath, err)
			if p == "" {
				return err
			}
			records <- connectors.NewError(realpath, err)
			return nil
		}

		finfo := objects.FileInfo{
			Lname:    path.Base(realpath),
			LmodTime: de.ModTime(ctx),
		}

		var open func() (io.ReadCloser, error)

		switch e := de.(type) {
		case rclonefs.Object:
			finfo.Lsize = e.Size()
			finfo.Lmode = 0600
			open = func() (io.ReadCloser, error) {
				return e.Open(ctx)
			}
		case rclonefs.Directory:
			finfo.Lmode = 0700 | fs.ModeDir
		}

		records <- connectors.NewRecord(realpath, "", finfo, nil, open)
		return nil
	})
	wg.Wait()
	return err
}

func (r *Rclone) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	f, err := r.openfs(ctx)
	if err != nil {
		return err
	}

	for record := range records {
		if record.Err != nil || record.IsXattr || record.Pathname == "/" {
			results <- record.Ok()
			continue
		}

		if record.FileInfo.Mode().IsRegular() {
			obj, err := f.Put(ctx, record.Reader, &objectinfo{record})
			if obj != nil && err != nil {
				// put may fail to completely create the object
				err = fmt.Errorf("failed to write %q: %w",
					record.Pathname, err)
			} else if err != nil {
				err = fmt.Errorf("failed to create %q: %w",
					record.Pathname, err)
			}

			results <- record.Error(err)
			continue
		}

		if record.FileInfo.IsDir() {
			err := f.Mkdir(ctx, record.Pathname)
			results <- record.Error(err)
			continue
		}

		// we don't support exporting other file types
		results <- record.Ok()
	}

	return nil
}

func (r *Rclone) Create(ctx context.Context, conf []byte) error {
	f, err := r.openfs(ctx)
	if err != nil {
		return err
	}

	_, err = f.NewObject(ctx, "CONFIG")
	if err == nil {
		return fmt.Errorf("kloset already initialized")
	}

	obj, err := f.Put(ctx, bytes.NewReader(conf), &objectinfo{&connectors.Record{
		Pathname: "CONFIG",
		FileInfo: objects.FileInfo{
			Lname:    "CONFIG",
			Lsize:    int64(len(conf)),
			Lmode:    0600,
			LmodTime: time.Now(),
		},
	}})
	if obj != nil && err != nil {
		return fmt.Errorf("failed to completely write CONFIG: %w", err)
	}
	if err != nil {
		return fmt.Errorf("failed to create CONFIG: %w", err)
	}

	for _, dir := range []string{"packfiles", "states", "locks"} {
		if err := f.Mkdir(ctx, dir); err != nil {
			return fmt.Errorf("failed to mkdir %s: %w", dir, err)
		}
	}

	return nil
}

func (r *Rclone) Open(ctx context.Context) ([]byte, error) {
	f, err := r.openfs(ctx)
	if err != nil {
		return nil, err
	}

	obj, err := f.NewObject(ctx, "CONFIG")
	if err != nil {
		return nil, fmt.Errorf("can't open CONFIG: %w", err)
	}

	rd, err := obj.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't open CONFIG: %w", err)
	}
	defer rd.Close()

	buf, err := io.ReadAll(rd)
	if err != nil {
		return nil, fmt.Errorf("failed to read CONFIG: %w", err)
	}
	return buf, nil
}

func (r *Rclone) Mode(ctx context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (r *Rclone) Size(ctx context.Context) (int64, error) {
	return -1, nil
}

func (r *Rclone) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	var dir string
	switch res {
	case storage.StorageResourcePackfile:
		dir = "packfiles"
	case storage.StorageResourceState:
		dir = "states"
	case storage.StorageResourceLock:
		dir = "locks"
	default:
		return nil, errors.ErrUnsupported
	}

	f, err := r.openfs(ctx)
	if err != nil {
		return nil, err
	}

	dirents, err := f.List(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", dir, err)
	}

	var macs []objects.MAC
	for _, dirent := range dirents {
		mac, err := hex.DecodeString(path.Base(dirent.Remote()))
		if err != nil {
			return nil, fmt.Errorf("failed to decode MAC %s: %w", dirent.Remote(), err)
		}

		macs = append(macs, objects.MAC(mac))
	}

	return macs, nil
}

func (r *Rclone) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	var target string

	switch res {
	case storage.StorageResourcePackfile:
		target = fmt.Sprintf("packfiles/%064x", mac)
	case storage.StorageResourceState:
		target = fmt.Sprintf("states/%064x", mac)
	case storage.StorageResourceLock:
		target = fmt.Sprintf("locks/%064x", mac)
	default:
		return -1, errors.ErrUnsupported
	}

	f, err := r.openfs(ctx)
	if err != nil {
		return -1, err
	}

	obj, err := f.Put(ctx, rd, &objectinfo{&connectors.Record{
		Pathname: target,
		FileInfo: objects.FileInfo{
			Lname:    path.Base(target),
			Lsize:    -1,
			Lmode:    0600,
			LmodTime: time.Now(),
		},
	}})
	if obj != nil && err != nil {
		return -1, fmt.Errorf("failed to completely write %s: %w", target, err)
	}
	if err != nil {
		return -1, fmt.Errorf("failed to write %s: %w", target, err)
	}
	return obj.Size(), nil
}

func (r *Rclone) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	var target string

	switch res {
	case storage.StorageResourcePackfile:
		target = fmt.Sprintf("packfiles/%064x", mac)
	case storage.StorageResourceState:
		target = fmt.Sprintf("states/%064x", mac)
	case storage.StorageResourceLock:
		target = fmt.Sprintf("locks/%064x", mac)
	default:
		return nil, errors.ErrUnsupported
	}

	f, err := r.openfs(ctx)
	if err != nil {
		return nil, err
	}

	obj, err := f.NewObject(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("can't open %s: %w", target, err)
	}

	var oo []rclonefs.OpenOption
	if rg != nil {
		oo = append(oo, &rclonefs.RangeOption{
			Start: int64(rg.Offset),
			End:   int64(rg.Offset) + int64(rg.Length),
		})
	}

	rd, err := obj.Open(ctx, oo...)
	if err != nil {
		return nil, fmt.Errorf("can't open %s: %w", target, err)
	}

	return rd, nil
}

func (r *Rclone) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	var target string

	switch res {
	case storage.StorageResourcePackfile:
		target = fmt.Sprintf("packfiles/%064x", mac)
	case storage.StorageResourceState:
		target = fmt.Sprintf("states/%064x", mac)
	case storage.StorageResourceLock:
		target = fmt.Sprintf("locks/%064x", mac)
	default:
		return errors.ErrUnsupported
	}

	f, err := r.openfs(ctx)
	if err != nil {
		return err
	}

	obj, err := f.NewObject(ctx, target)
	if err != nil {
		return fmt.Errorf("can't open %s: %w", target, err)
	}

	if err := obj.Remove(ctx); err != nil {
		return fmt.Errorf("failed to remove %s: %w", target, err)
	}

	return nil
}

func (r *Rclone) Close(ctx context.Context) error {
	return nil
}
