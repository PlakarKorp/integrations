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

	plakarsmb "github.com/PlakarKorp/integrations/smb/common"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
)

func init() {
	_ = storage.Register("smb", 0, NewStore)
}

type Store struct {
	cfg  *plakarsmb.Config
	conn *plakarsmb.Conn

	packfiles Buckets
	states    Buckets
}

func NewStore(ctx context.Context, proto string, storeConfig map[string]string) (storage.Store, error) {
	cfg, err := plakarsmb.ParseConfig(storeConfig)
	if err != nil {
		return nil, err
	}

	conn, err := plakarsmb.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return &Store{cfg: cfg, conn: conn}, nil
}

func (s *Store) Origin() string        { return s.cfg.Origin() }
func (s *Store) Root() string          { return s.cfg.Root }
func (s *Store) Type() string          { return "smb" }
func (s *Store) Flags() location.Flags { return 0 }

func (s *Store) Mode(context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

// Size is unknown: computing it would mean walking every bucket directory
// over the network.
func (s *Store) Size(context.Context) (int64, error) {
	return -1, nil
}

// Path joins args onto the store's configured root within the share.
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

	if err := s.conn.MkdirAll(s.Path("locks"), 0o700); err != nil {
		return err
	}

	_, err := writeFileAtomic(s.conn, s.Path("CONFIG"), bytes.NewReader(config), false)
	return err
}

// ensureEmptyDir creates p if missing, or confirms it is an empty directory.
func (s *Store) ensureEmptyDir(p string) error {
	fi, err := s.conn.Lstat(p)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return s.conn.MkdirAll(p, 0o700)
	}
	if !fi.IsDir() {
		return errors.New("storage root is not a directory")
	}

	entries, err := s.conn.ReadDir(p)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("storage root is not empty")
	}
	return nil
}

func (s *Store) Open(ctx context.Context) ([]byte, error) {
	s.packfiles = NewBuckets(s.conn, s.Path("packfiles"))
	s.states = NewBuckets(s.conn, s.Path("states"))

	rd, err := s.conn.Open(s.Path("CONFIG"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }()

	return io.ReadAll(rd)
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.conn.Lstat(s.Path())
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
		return s.getLocks()
	default:
		return nil, errors.ErrUnsupported
	}
}

func (s *Store) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return s.packfiles.Put(mac, rd)
	case storage.StorageResourceState:
		return s.states.Put(mac, rd)
	case storage.StorageResourceLock:
		return writeFileAtomic(s.conn, s.lockPath(mac), rd, true)
	default:
		return -1, errors.ErrUnsupported
	}
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
		return s.conn.Open(s.lockPath(mac))
	default:
		return nil, errors.ErrUnsupported
	}
}

func (s *Store) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	switch res {
	case storage.StorageResourcePackfile:
		return s.packfiles.Remove(mac)
	case storage.StorageResourceState:
		return s.states.Remove(mac)
	case storage.StorageResourceLock:
		return s.conn.Remove(s.lockPath(mac))
	default:
		return errors.ErrUnsupported
	}
}

func (s *Store) lockPath(mac objects.MAC) string {
	return path.Join(s.Path("locks"), hex.EncodeToString(mac[:]))
}

func (s *Store) getLocks() ([]objects.MAC, error) {
	entries, err := s.conn.ReadDir(s.Path("locks"))
	if err != nil {
		return nil, err
	}
	return macsFromEntries(entries), nil
}
