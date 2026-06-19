package rclone

import (
	"context"

	"github.com/rclone/rclone/fs"
	"golang.org/x/sync/errgroup"
)

func pwalk(ctx context.Context, f fs.Fs, wg *errgroup.Group, dir string, fn func(string, fs.DirEntry, error) error) error {
	dirents, err := f.List(ctx, dir)
	if err != nil {
		return fn(dir, nil, err)
	}

	for _, dirent := range dirents {
		if err := fn(dirent.Remote(), dirent, nil); err != nil {
			return err
		}

		if _, ok := dirent.(fs.Directory); ok {
			wg.Go(func() error {
				pwalk(ctx, f, wg, dirent.Remote(), fn)
				return nil
			})
		}
	}

	return nil
}
