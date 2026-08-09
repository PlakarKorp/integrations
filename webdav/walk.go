package webdav

import (
	"context"
	"io/fs"
	"path"

	"github.com/emersion/go-webdav"
)

type walkDirFunc func(path string, finfo *webdav.FileInfo, err error) error

func (w *WebDAV) walkdir(ctx context.Context, finfo *webdav.FileInfo, fn walkDirFunc) error {
	if err := fn(finfo.Path, finfo, nil); err != nil {
		return err
	}

	if !finfo.IsDir {
		return nil
	}

	dirents, err := w.client.ReadDir(ctx, finfo.Path, false)
	if err != nil {
		return fn(finfo.Path, nil, err)
	}

	for i := range dirents {
		// readdir seems to return the directory itself, too
		dirents[i].Path = path.Clean(dirents[i].Path)
		if dirents[i].Path == finfo.Path {
			continue
		}

		if err := w.walkdir(ctx, &dirents[i], fn); err != nil {
			if err == fs.SkipDir {
				continue
			}
			return err
		}
	}

	return nil
}

func (w *WebDAV) walk(ctx context.Context, fn walkDirFunc) error {
	sb, err := w.client.Stat(ctx, w.location.Path)
	if err != nil {
		return fn(w.location.Path, nil, err)
	}

	sb.Path = path.Clean(sb.Path)

	if err = w.walkdir(ctx, sb, fn); err != nil {
		if err == fs.SkipDir || err == fs.SkipAll {
			err = nil
		}
	}

	return err
}
