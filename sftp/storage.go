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

package sftp

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
)

func (s *Sftp) path(args ...string) string {
	return path.Join(s.rootDir, path.Join(args...))
}

func (s *Sftp) getLocks() (ret []objects.MAC, err error) {
	entries, err := s.client.ReadDir(s.path("locks"))
	if err != nil {
		return
	}

	for i := range entries {
		var t []byte
		t, err = hex.DecodeString(entries[i].Name())
		if err != nil {
			continue
		}
		if len(t) != 32 {
			continue
		}
		ret = append(ret, objects.MAC(t))
	}
	return
}

func (s *Sftp) getLock(lockID objects.MAC) (io.ReadCloser, error) {
	fp, err := s.client.Open(path.Join(s.path("locks"), hex.EncodeToString(lockID[:])))
	if err != nil {
		return nil, err
	}

	return fp, nil
}

func (s *Sftp) Create(ctx context.Context, config []byte) error {
	dirfp, err := s.client.ReadDir(s.path())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		err = s.client.MkdirAll(s.path())
		if err != nil {
			return err
		}
		err = s.client.Chmod(s.path(), 0700)
		if err != nil {
			return err
		}
	} else {
		if len(dirfp) > 0 {
			return fmt.Errorf("directory %s is not empty", s.rootDir)
		}
	}
	s.packfiles = NewBuckets(s.client, s.path("packfiles"))
	if err := s.packfiles.Create(); err != nil {
		return err
	}

	s.states = NewBuckets(s.client, s.path("states"))
	if err := s.states.Create(); err != nil {
		return err
	}

	err = s.client.Mkdir(s.path("locks"))
	if err != nil {
		return err
	}

	_, err = writeFileAtomic(s.client, s.path("CONFIG"), bytes.NewReader(config))
	return err
}

func (s *Sftp) Open(ctx context.Context) ([]byte, error) {
	rd, err := s.client.Open(s.path("CONFIG"))
	if err != nil {
		return nil, err
	}
	defer rd.Close() // do we care about err?

	data, err := io.ReadAll(rd)
	if err != nil {
		return nil, err
	}

	s.packfiles = NewBuckets(s.client, s.path("packfiles"))

	s.states = NewBuckets(s.client, s.path("states"))

	return data, nil
}

func (s *Sftp) Mode(ctx context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (s *Sftp) Size(ctx context.Context) (int64, error) {
	return -1, nil
}

func (s *Sftp) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
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

func (s *Sftp) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return s.packfiles.Get(mac, rg)
	case storage.StorageResourceState:
		if rg != nil {
			return nil, errors.ErrUnsupported
		}
		return s.states.Get(mac, nil)
	case storage.StorageResourceLock:
		if rg != nil {
			return nil, errors.ErrUnsupported
		}
		return s.getLock(mac)
	default:
		return nil, errors.ErrUnsupported
	}
}

func (s *Sftp) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return s.packfiles.Put(mac, rd)
	case storage.StorageResourceState:
		return s.states.Put(mac, rd)
	case storage.StorageResourceLock:
		return writeFileAtomic(s.client, path.Join(s.path("locks"), hex.EncodeToString(mac[:])), rd)
	default:
		return -1, errors.ErrUnsupported
	}
}

func (s *Sftp) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	switch res {
	case storage.StorageResourcePackfile:
		return s.packfiles.Remove(mac)
	case storage.StorageResourceState:
		return s.states.Remove(mac)
	case storage.StorageResourceLock:
		return s.client.Remove(path.Join(s.path("locks"), hex.EncodeToString(mac[:])))
	default:
		return errors.ErrUnsupported
	}
}
