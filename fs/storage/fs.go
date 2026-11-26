/*
 * Copyright (c) 2021 Gilles Chehade <gilles@poolp.org>
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

package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"strings"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
)

type Store struct {
	location  string
	packfiles Buckets
	states    Buckets
}

var ErrPackfileNotFound = errors.New("packfile not found")

func init() {
	_ = integration.RegisterStore("fs", location.FLAG_LOCALFS, NewStore)
}

func NewStore(ctx context.Context, proto string, storeConfig map[string]string) (integration.Store, error) {
	return &Store{
		location: strings.TrimPrefix(storeConfig["location"], proto+"://"),
	}, nil
}

func (s *Store) Origin() string        { return "localhost" }
func (s *Store) Root() string          { return s.location }
func (s *Store) Type() string          { return "fs" }
func (s *Store) Flags() location.Flags { return location.FLAG_LOCALFS }

func (s *Store) Mode(context.Context) (integration.Mode, error) {
	return integration.ModeRead | integration.ModeWrite, nil
}

func (s *Store) Path(args ...string) string {
	args = append(args, "")
	copy(args[1:], args)
	args[0] = s.location

	return filepath.Join(args...)
}

func (s *Store) Create(ctx context.Context, config []byte) error {
	dirfp, err := os.Open(s.Path())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		err = os.MkdirAll(s.Path(), 0700)
		if err != nil {
			return err
		}
	} else {
		defer func() { _ = dirfp.Close() }()
		entries, err := dirfp.Readdir(1)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("directory %s is not empty", s.Path())
		}
	}

	s.packfiles = NewBuckets(s.Path("packfiles"))
	if err := s.packfiles.Create(); err != nil {
		return err
	}

	s.states = NewBuckets(s.Path("states"))
	if err := s.states.Create(); err != nil {
		return err
	}

	err = os.Mkdir(s.Path("locks"), 0700)
	if err != nil {
		return err
	}

	_, err = WriteToFileAtomic(s.Path("CONFIG"), bytes.NewReader(config))
	return err
}

func (s *Store) Open(ctx context.Context) ([]byte, error) {
	s.packfiles = NewBuckets(s.Path("packfiles"))
	s.states = NewBuckets(s.Path("states"))

	rd, err := os.Open(s.Path("CONFIG"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }() // do we care about err?

	data, err := io.ReadAll(rd)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return nil
}

func (s *Store) Size(ctx context.Context) (int64, error) {
	var size int64
	location := strings.TrimPrefix(s.location, "fs://")
	err := filepath.WalkDir(location, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func readdir(dir string) iter.Seq2[os.DirEntry, error] {
	return func(yield func(os.DirEntry, error) bool) {
		dp, err := os.Open(dir)
		if err != nil {
			yield(nil, err)
			return
		}
		defer func() { _ = dp.Close() }()

		for {
			entries, err := dp.ReadDir(32)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}

			for i := range entries {
				if n := entries[i].Name(); n == "." || n == ".." {
					continue
				}

				if !yield(entries[i], nil) {
					return
				}
			}
		}
	}
}

func (s *Store) List(ctx context.Context, res integration.StorageResource, flag integration.StorageFlag) iter.Seq2[*integration.StorageResourceEntry, error] {
	switch res {
	case integration.StorageResourcePackfile:
		return s.packfiles.List()
	case integration.StorageResourceState:
		return s.states.List()
	case integration.StorageResourceLock:
		return s.getLocks()
	default:
		return func(yield func(*integration.StorageResourceEntry, error) bool) {
			yield(nil, errors.ErrUnsupported)
		}
	}
}

func (s *Store) Put(ctx context.Context, res integration.StorageResource, flag integration.StorageFlag, mac integration.MAC, rd io.Reader) (int64, error) {
	switch res {
	case integration.StorageResourcePackfile:
		return s.packfiles.Put(mac, rd)
	case integration.StorageResourceState:
		return s.states.Put(mac, rd)
	case integration.StorageResourceLock:
		return s.putLock(mac, rd)
	default:
		return -1, errors.ErrUnsupported
	}
}

func (s *Store) Get(ctx context.Context, res integration.StorageResource, flag integration.StorageFlag, mac integration.MAC, rg *integration.Range) (io.ReadCloser, error) {
	switch res {
	case integration.StorageResourcePackfile:
		var fp io.ReadCloser
		var err error
		if rg == nil {
			fp, err = s.packfiles.Get(mac)
		} else {
			fp, err = s.packfiles.GetBlob(mac, rg.Offset, rg.Length)
		}

		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				err = ErrPackfileNotFound
			}
			return nil, err
		}

		return fp, err
	case integration.StorageResourceState:
		return s.states.Get(mac)
	case integration.StorageResourceLock:
		return s.getLock(mac)
	default:
		return nil, errors.ErrUnsupported
	}
}

func (s *Store) Delete(ctx context.Context, res integration.StorageResource, flag integration.StorageFlag, mac integration.MAC) error {
	switch res {
	case integration.StorageResourcePackfile:
		return s.packfiles.Remove(mac)
	case integration.StorageResourceState:
		return s.states.Remove(mac)
	case integration.StorageResourceLock:
		return s.deleteLock(mac)
	default:
		return errors.ErrUnsupported
	}
}

func (s *Store) Close(ctx context.Context) error {
	return nil
}

func (s *Store) getLocks() iter.Seq2[*integration.StorageResourceEntry, error] {
	return func(yield func(*integration.StorageResourceEntry, error) bool) {
		for lock, err := range readdir(s.Path("locks")) {
			if err != nil {
				yield(nil, err)
				return
			}

			if !lock.Type().IsRegular() {
				continue
			}

			lockID, err := hex.DecodeString(lock.Name())
			if err != nil {
				continue
			}

			if len(lockID) != 32 {
				continue
			}

			info, err := lock.Info()
			if errors.Is(err, fs.ErrNotExist) {
				// removed since ReadDir
				continue
			}
			if err != nil {
				yield(nil, err)
				return
			}

			e := integration.StorageResourceEntry{
				MAC:   integration.MAC(lockID),
				Size:  info.Size(),
				MTime: info.ModTime(),
			}
			if !yield(&e, nil) {
				return
			}
		}
	}
}

func (s *Store) putLock(lockID integration.MAC, rd io.Reader) (int64, error) {
	return WriteToFileAtomicTempDir(filepath.Join(s.Path("locks"), hex.EncodeToString(lockID[:])), rd, s.Path(""))
}

func (s *Store) getLock(lockID integration.MAC) (io.ReadCloser, error) {
	fp, err := os.Open(filepath.Join(s.Path("locks"), hex.EncodeToString(lockID[:])))
	if err != nil {
		return nil, err
	}

	return fp, nil
}

func (s *Store) deleteLock(lockID integration.MAC) error {
	if err := os.Remove(filepath.Join(s.Path("locks"), hex.EncodeToString(lockID[:]))); err != nil {
		return err
	}

	return nil
}
