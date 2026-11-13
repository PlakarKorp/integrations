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
	"iter"
	"path"

	"github.com/PlakarKorp/integration-sdk"
)

func (s *Sftp) path(args ...string) string {
	return path.Join(s.rootDir, path.Join(args...))
}

func (s *Sftp) getLocks() (ret []integration.MAC, err error) {
	entries, err := s.client.ReadDir(s.path("locks"))
	if err != nil {
		return
	}

	for i := range entries {
		var t []byte
		t, err := hex.DecodeString(entries[i].Name())
		if err != nil {
			continue
		}
		if len(t) != 32 {
			continue
		}
		ret = append(ret, integration.MAC(t))
	}
	return
}

func (s *Sftp) getLock(lockID integration.MAC) (io.ReadCloser, error) {
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
	s.packfiles = newBuckets(s.client, s.path("packfiles"))
	if err := s.packfiles.Create(); err != nil {
		return err
	}

	s.states = newBuckets(s.client, s.path("states"))
	if err := s.states.Create(); err != nil {
		return err
	}

	err = s.client.Mkdir(s.path("locks"))
	if err != nil {
		return err
	}

	_, err = writeFileAtomic(s.client, s.path("CONFIG"), bytes.NewReader(config), nil)
	return err
}

func (s *Sftp) Open(ctx context.Context) ([]byte, error) {
	rd, err := s.client.Open(s.path("CONFIG"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }() // do we care about err?

	data, err := io.ReadAll(rd)
	if err != nil {
		return nil, err
	}

	s.packfiles = newBuckets(s.client, s.path("packfiles"))

	s.states = newBuckets(s.client, s.path("states"))

	return data, nil
}

func (s *Sftp) Mode(ctx context.Context) (integration.Mode, error) {
	return integration.ModeRead | integration.ModeWrite, nil
}

func (s *Sftp) Size(ctx context.Context) (int64, error) {
	return -1, nil
}

func (s *Sftp) List(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag) iter.Seq2[*integration.StorageResourceEntry, error] {
	return func(yield func(*integration.StorageResourceEntry, error) bool) {
		var macs []integration.MAC
		var err error

		switch res {
		case integration.StorageResourcePackfile:
			macs, err = s.packfiles.List()
		case integration.StorageResourceState:
			macs, err = s.states.List()
		case integration.StorageResourceLock:
			macs, err = s.getLocks()
		default:
			err = errors.ErrUnsupported
		}
		if err != nil {
			yield(nil, err)
			return
		}

		for _, mac := range macs {
			if !yield(&integration.StorageResourceEntry{MAC: mac}, nil) {
				return
			}
		}
	}
}

func (s *Sftp) Get(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag, mac integration.MAC, rg *integration.Range) (io.ReadCloser, error) {
	switch res {
	case integration.StorageResourcePackfile:
		return s.packfiles.Get(mac, rg)
	case integration.StorageResourceState:
		if rg != nil {
			return nil, errors.ErrUnsupported
		}
		return s.states.Get(mac, nil)
	case integration.StorageResourceLock:
		if rg != nil {
			return nil, errors.ErrUnsupported
		}
		return s.getLock(mac)
	default:
		return nil, errors.ErrUnsupported
	}
}

func (s *Sftp) Put(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag, mac integration.MAC, rd io.Reader) (int64, error) {
	// OpenSSH in readonly mode doesn't expose this extension, yet it's okay to
	// use it as a storage, so we can't check at init time, we have to check on the
	// first Put call.
	if _, ok := s.client.HasExtension("posix-rename@openssh.com"); !ok {
		return 0, fmt.Errorf("%q does not support the posix-rename@openssh.com extension, required for storage", s.endpoint.Host)
	}

	switch res {
	case integration.StorageResourcePackfile:
		return s.packfiles.Put(mac, rd)
	case integration.StorageResourceState:
		return s.states.Put(mac, rd)
	case integration.StorageResourceLock:
		return writeFileAtomic(s.client, path.Join(s.path("locks"), hex.EncodeToString(mac[:])), rd, nil)
	default:
		return -1, errors.ErrUnsupported
	}
}

func (s *Sftp) Delete(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag, mac integration.MAC) error {
	switch res {
	case integration.StorageResourcePackfile:
		return s.packfiles.Remove(mac)
	case integration.StorageResourceState:
		return s.states.Remove(mac)
	case integration.StorageResourceLock:
		return s.client.Remove(path.Join(s.path("locks"), hex.EncodeToString(mac[:])))
	default:
		return errors.ErrUnsupported
	}
}
