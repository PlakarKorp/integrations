/*
 * Copyright (c) 2025 Gilles Chehade <gilles@poolp.org>
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
	"io"
	"io/fs"
	"path"

	plakarnfs "github.com/PlakarKorp/integrations/nfs/common"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
)

func init() {
	_ = storage.Register("nfs", 0, NewStore)
}

type Store struct {
	cfg  *plakarnfs.Config
	conn *plakarnfs.Conn

	packfiles Buckets
	states    Buckets
}

func NewStore(ctx context.Context, proto string, storeConfig map[string]string) (storage.Store, error) {
	cfg, err := plakarnfs.ParseConfig(storeConfig)
	if err != nil {
		return nil, err
	}

	conn, err := plakarnfs.Connect(cfg)
	if err != nil {
		return nil, err
	}

	return &Store{cfg: cfg, conn: conn}, nil
}

func (s *Store) Origin() string        { return s.cfg.Origin() }
func (s *Store) Root() string          { return s.cfg.Root }
func (s *Store) Type() string          { return "nfs" }
func (s *Store) Flags() location.Flags { return 0 }

func (s *Store) Mode(context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

// Size is unknown: computing it would mean walking every bucket directory
// over the network, which the interface already warns is costly even for a
// local filesystem.
func (s *Store) Size(context.Context) (int64, error) {
	return -1, nil
}

// Path joins args onto the store's configured root.
func (s *Store) Path(args ...string) string {
	return path.Join(append([]string{s.cfg.Root}, args...)...)
}

func (s *Store) Create(ctx context.Context, config []byte) error {
	if err := s.ensureEmptyDir(s.Path()); err != nil {
		return err
	}

	s.packfiles = NewBuckets(s.conn, s.Path("packfiles"))
	if err := s.packfiles.Create(); err != nil {
		return err
	}

	s.states = NewBuckets(s.conn, s.Path("states"))
	if err := s.states.Create(); err != nil {
		return err
	}

	if err := s.conn.Mkdir(s.Path("locks"), 0o700); err != nil {
		return err
	}

	_, err := writeFileAtomic(s.conn, s.Path("CONFIG"), s.Path(), 0o600, bytes.NewReader(config))
	return err
}

// ensureEmptyDir creates p if missing, or confirms it's an empty directory.
func (s *Store) ensureEmptyDir(p string) error {
	attr, _, err := s.conn.Lookup(p)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return mkdirAll(s.conn, p, 0o700)
	}
	if attr != nil && !plakarnfs.IsDir(attr) {
		return errors.New("storage root is not a directory")
	}

	entries, err := s.conn.ReadDirPlus(p)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.FileName == "." || entry.FileName == ".." {
			continue
		}
		return errors.New("storage root is not empty")
	}
	return nil
}

func (s *Store) Open(ctx context.Context) ([]byte, error) {
	s.packfiles = NewBuckets(s.conn, s.Path("packfiles"))
	s.states = NewBuckets(s.conn, s.Path("states"))

	rd, err := s.conn.OpenReader(s.Path("CONFIG"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }()

	return io.ReadAll(rd)
}

func (s *Store) Ping(ctx context.Context) error {
	_, _, err := s.conn.Lookup(s.Path())
	return err
}

func (s *Store) Close(ctx context.Context) error {
	return s.conn.Close()
}

func (s *Store) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return s.packfiles.List()
	case storage.StorageResourceState:
		return s.states.List()
	case storage.StorageResourceLock:
		return s.getLocks(ctx)
	}
	return nil, errors.ErrUnsupported
}

func (s *Store) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return s.packfiles.Put(mac, rd)
	case storage.StorageResourceState:
		return s.states.Put(mac, rd)
	case storage.StorageResourceLock:
		return s.putLock(ctx, mac, rd)
	}
	return -1, errors.ErrUnsupported
}

func (s *Store) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	switch res {
	case storage.StorageResourcePackfile:
		var fp io.ReadCloser
		var err error
		if rg == nil {
			fp, err = s.packfiles.Get(mac)
		} else {
			fp, err = s.packfiles.GetRange(mac, rg)
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				err = repository.ErrPackfileNotFound
			}
			return nil, err
		}
		return fp, nil
	case storage.StorageResourceState:
		if rg != nil {
			return nil, errors.ErrUnsupported
		}
		return s.states.Get(mac)
	case storage.StorageResourceLock:
		if rg != nil {
			return nil, errors.ErrUnsupported
		}
		return s.getLock(ctx, mac)
	}
	return nil, errors.ErrUnsupported
}

func (s *Store) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	switch res {
	case storage.StorageResourcePackfile:
		return s.packfiles.Remove(mac)
	case storage.StorageResourceState:
		return s.states.Remove(mac)
	case storage.StorageResourceLock:
		return s.deleteLock(ctx, mac)
	}
	return errors.ErrUnsupported
}

func (s *Store) getLocks(ctx context.Context) ([]objects.MAC, error) {
	ret := make([]objects.MAC, 0)

	entries, err := s.conn.ReadDirPlus(s.Path("locks"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.FileName == "." || entry.FileName == ".." || entry.IsDir() {
			continue
		}
		lockID, err := hex.DecodeString(entry.FileName)
		if err != nil || len(lockID) != 32 {
			continue
		}
		ret = append(ret, objects.MAC(lockID))
	}
	return ret, nil
}

func (s *Store) putLock(ctx context.Context, lockID objects.MAC, rd io.Reader) (int64, error) {
	locksDir := s.Path("locks")
	return writeFileAtomic(s.conn, path.Join(locksDir, hex.EncodeToString(lockID[:])), locksDir, 0o600, rd)
}

func (s *Store) getLock(ctx context.Context, lockID objects.MAC) (io.ReadCloser, error) {
	return s.conn.OpenReader(path.Join(s.Path("locks"), hex.EncodeToString(lockID[:])))
}

func (s *Store) deleteLock(ctx context.Context, lockID objects.MAC) error {
	return s.conn.Remove(path.Join(s.Path("locks"), hex.EncodeToString(lockID[:])))
}
