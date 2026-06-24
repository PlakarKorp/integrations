/*
 * Copyright (c) 2026 Omar Polo <op@omarpolo.com>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package rclone

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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
	"github.com/rclone/rclone/fs/walk"

	_ "github.com/rclone/rclone/backend/all" // register backends
)

type Rclone struct {
	typ         string
	base        string
	concurrency int
	fs          rclonefs.Fs
	spool       bool
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

	f, err := rclonefs.NewFs(ctx, fmt.Sprintf("%s:%s", typ, base))
	if err != nil {
		return nil, fmt.Errorf("failed to create rclone fs: %w", err)
	}

	var spool bool
	if _, ok := f.(rclonefs.PutStreamer); !ok {
		// this backend does not support uploading without
		// knowing in advance the size.
		spool = true
	}

	return &Rclone{
		typ:         typ,
		base:        base,
		concurrency: opts.MaxConcurrency,
		fs:          f,
		spool:       spool,
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

func (r *Rclone) Ping(ctx context.Context) error {
	_, err := r.fs.List(ctx, "")
	return err
}

func (r *Rclone) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	return walk.Walk(ctx, r.fs, "", true, -1, func(p string, des rclonefs.DirEntries, err error) error {
		if err != nil {
			realpath := path.Join(r.base, p)
			err = fmt.Errorf("failed to walk %s: %w", realpath, err)
			if p == "" {
				return err
			}
			records <- connectors.NewError(realpath, err)
			return nil
		}

		for _, de := range des {
			realpath := path.Join(r.base, de.Remote())

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
		}

		return nil
	})
}

func (r *Rclone) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for record := range records {
		if record.Err != nil || record.IsXattr || record.Pathname == "/" {
			results <- record.Ok()
			continue
		}

		if record.FileInfo.Mode().IsRegular() {
			obj, err := r.fs.Put(ctx, record.Reader, &objectinfo{record})
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
			err := r.fs.Mkdir(ctx, record.Pathname)
			results <- record.Error(err)
			continue
		}

		// we don't support exporting other file types
		results <- record.Ok()
	}

	return nil
}

func (r *Rclone) Create(ctx context.Context, conf []byte) error {
	_, err := r.fs.NewObject(ctx, "CONFIG")
	if err == nil {
		return fmt.Errorf("kloset already initialized")
	}
	if err != nil && !errors.Is(err, rclonefs.ErrorObjectNotFound) {
		return fmt.Errorf("failed to check whether CONFIG exists: %w", err)
	}

	obj, err := r.fs.Put(ctx, bytes.NewReader(conf), &objectinfo{&connectors.Record{
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
		if err := r.fs.Mkdir(ctx, dir); err != nil {
			return fmt.Errorf("failed to mkdir %s: %w", dir, err)
		}
	}

	return nil
}

func (r *Rclone) Open(ctx context.Context) ([]byte, error) {
	obj, err := r.fs.NewObject(ctx, "CONFIG")
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

func resdir(res storage.StorageResource) (string, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return "packfiles", nil
	case storage.StorageResourceState:
		return "states", nil
	case storage.StorageResourceLock:
		return "locks", nil
	default:
		return "", errors.ErrUnsupported
	}
}

func respath(res storage.StorageResource, mac objects.MAC) (string, error) {
	dir, err := resdir(res)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%x", dir, mac), nil
}

func (r *Rclone) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	dir, err := resdir(res)
	if err != nil {
		return nil, err
	}

	dirents, err := r.fs.List(ctx, dir)
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
	target, err := respath(res, mac)
	if err != nil {
		return -1, err
	}

	var size int64 = -1
	if r.spool {
		if res == storage.StorageResourceLock {
			// locks are small enough that can fit in
			// memory.
			body, err := io.ReadAll(rd)
			if err != nil {
				return -1, fmt.Errorf("failed to read lock: %w", err)
			}
			size = int64(len(body))
			rd = bytes.NewReader(body)
		} else {
			fp, err := os.CreateTemp("", "plakar-rclone-*")
			if err != nil {
				return -1, fmt.Errorf("failed to create temp file: %w", err)
			}
			defer os.Remove(fp.Name())
			defer fp.Close()

			n, err := io.Copy(fp, rd)
			if err != nil {
				return -1, fmt.Errorf("failed to write to temp file: %w", err)
			}

			if _, err := fp.Seek(0, io.SeekStart); err != nil {
				return -1, fmt.Errorf("failed to seek: %w", err)
			}

			size = n
			rd = fp
		}
	}

	obj, err := r.fs.Put(ctx, rd, &objectinfo{&connectors.Record{
		Pathname: target,
		FileInfo: objects.FileInfo{
			Lname:    path.Base(target),
			Lsize:    size,
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
	target, err := respath(res, mac)
	if err != nil {
		return nil, err
	}

	obj, err := r.fs.NewObject(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("can't open %s: %w", target, err)
	}

	var oo []rclonefs.OpenOption
	if rg != nil {
		oo = append(oo, &rclonefs.RangeOption{
			Start: int64(rg.Offset),
			End:   int64(rg.Offset) + int64(rg.Length) - 1,
		})
	}

	rd, err := obj.Open(ctx, oo...)
	if err != nil {
		return nil, fmt.Errorf("can't open %s: %w", target, err)
	}

	return rd, nil
}

func (r *Rclone) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	target, err := respath(res, mac)
	if err != nil {
		return err
	}

	obj, err := r.fs.NewObject(ctx, target)
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
