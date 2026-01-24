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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/versioning"
	"github.com/dustin/go-humanize"
)

type Store struct {
	config     []byte
	Repository string
	location   string // What we were given on open, then split up in host and root
	host       string
	root       string

	mode storage.Mode

	fp ReadWriteSeekStatReadAtCloser

	configOffset int64
	configLength int64

	packfileOffset int64
	packfileLength int64

	stateOffset int64
	stateLength int64

	proto string
}

var stateMAC = objects.MAC{0x0f, 0x0e, 0x0d, 0x0c, 0x0b, 0x0a, 0x09, 0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01, 0x00}
var packfileMAC = objects.MAC{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}

func init() {
	storage.Register("ptar", location.FLAG_LOCALFS|location.FLAG_FILE, NewStore)
	storage.Register("ptar+http", location.FLAG_FILE, NewStore)
	storage.Register("ptar+https", location.FLAG_FILE, NewStore)
}

func NewStore(ctx context.Context, proto string, storeConfig map[string]string) (storage.Store, error) {
	return &Store{
		location: storeConfig["location"],
		proto:    proto,
	}, nil
}

func (s *Store) Create(ctx context.Context, config []byte) error {
	s.config = config
	s.mode = storage.ModeRead | storage.ModeWrite

	if s.proto != "ptar" {
		return fmt.Errorf("unsupported protocol: %s", s.proto)
	}

	s.host = "localhost"

	s.root = strings.TrimPrefix(s.location, "ptar://")
	fp, err := os.OpenFile(s.root, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	s.fp = fp

	fp.Write([]byte{'_', 'P', 'L', 'A', 'T', 'A', 'R', '_'})

	version := versioning.FromString("1.0.0")
	versionBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(versionBytes, uint32(version))
	fp.Write(versionBytes)

	fp.Write(config)

	s.configOffset = 12
	s.configLength = int64(len(config))
	return nil
}

func (s *Store) Open(ctx context.Context) ([]byte, error) {
	s.mode = storage.ModeRead

	var fp ReadWriteSeekStatReadAtCloser
	var err error

	switch s.proto {
	case "ptar":
		s.root = strings.TrimPrefix(s.location, "ptar://")
		s.host = "hostname"
		fp, err = os.Open(s.root)

	case "ptar+http", "ptar+https":
		url, err := url.Parse(s.location)
		if err != nil {
			return nil, fmt.Errorf("failed to parse url endpoint: %w", err)
		}

		s.host = url.Host
		s.root = url.Path

		location := strings.TrimPrefix(s.location, "ptar+")
		fp, err = NewHTTPReader(location)

	default:
		return nil, fmt.Errorf("unsupported protocol: %s", s.proto)
	}

	if err != nil {
		return nil, err
	}
	s.fp = fp

	magic := make([]byte, 8)
	_, err = io.ReadFull(fp, magic)
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(magic, []byte("_PLATAR_")) {
		return nil, storage.ErrInvalidMagic
	}

	versionBytes := make([]byte, 4)
	_, err = io.ReadFull(fp, versionBytes)
	if err != nil {
		return nil, err
	}

	_, err = fp.Seek(-48, io.SeekEnd)
	if err != nil {
		return nil, err
	}

	binary.Read(s.fp, binary.LittleEndian, &s.configOffset)
	binary.Read(s.fp, binary.LittleEndian, &s.configLength)
	binary.Read(s.fp, binary.LittleEndian, &s.packfileOffset)
	binary.Read(s.fp, binary.LittleEndian, &s.packfileLength)
	binary.Read(s.fp, binary.LittleEndian, &s.stateOffset)
	binary.Read(s.fp, binary.LittleEndian, &s.stateLength)

	_, err = fp.Seek(s.configOffset, io.SeekStart)
	if err != nil {
		return nil, err
	}

	if s.configLength <= 0 || s.configLength > 32*1024 {
		return nil, fmt.Errorf("invalid configuration length: %s, file corrupted", humanize.IBytes(uint64(s.configLength)))
	}

	config := make([]byte, s.configLength)
	_, err = io.ReadFull(fp, config)
	if err != nil {
		return nil, err
	}
	s.config = config

	return s.config, nil
}

func (s *Store) Ping(ctx context.Context) error {
	switch s.proto {
	case "ptar":
		location := strings.TrimPrefix(s.location, "ptar://")
		if _, err := os.Stat(location); err != nil {
			return err
		}

	case "ptar+http", "ptar+https":
		location := strings.TrimPrefix(s.location, "ptar+")
		if _, err := NewHTTPReader(location); err != nil {
			return err
		}

	default:
		return fmt.Errorf("unsupported protocol: %s", s.proto)
	}

	return nil
}

func (s *Store) Close(ctx context.Context) error {
	if s.mode&storage.ModeWrite != 0 {
		binary.Write(s.fp, binary.LittleEndian, s.configOffset)
		binary.Write(s.fp, binary.LittleEndian, s.configLength)
		binary.Write(s.fp, binary.LittleEndian, s.packfileOffset)
		binary.Write(s.fp, binary.LittleEndian, s.packfileLength)
		binary.Write(s.fp, binary.LittleEndian, s.stateOffset)
		binary.Write(s.fp, binary.LittleEndian, s.stateLength)
	}
	return s.fp.Close()
}

func (s *Store) Origin() string {
	return s.host
}

func (s *Store) Type() string {
	return "ptar"
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) Flags() location.Flags {
	switch s.proto {
	case "ptar":
		return location.FLAG_FILE | location.FLAG_LOCALFS
	case "ptar+http", "ptar+https":
		return location.FLAG_FILE
	default:
		return 0
	}
}

func (s *Store) Mode(context.Context) (storage.Mode, error) {
	return s.mode, nil
}

func (s *Store) Size(ctx context.Context) (int64, error) {
	fi, err := s.fp.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (s *Store) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	switch res {
	case storage.StorageResourceState:
		if s.mode&storage.ModeWrite != 0 {
			return []objects.MAC{}, nil
		}

		return []objects.MAC{
			stateMAC,
		}, nil
	case storage.StorageResourcePackfile:
		return []objects.MAC{
			packfileMAC,
		}, nil
	case storage.StorageResourceLock:
		return []objects.MAC{}, nil
	}

	return nil, errors.ErrUnsupported
}

func (s *Store) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	switch res {
	case storage.StorageResourceState:
		if s.mode&storage.ModeWrite == 0 {
			return 0, storage.ErrNotWritable
		}

		s.stateOffset = s.packfileOffset + s.packfileLength
		nbytes, err := io.Copy(s.fp, rd)
		if err != nil {
			return 0, err
		}
		s.stateLength = nbytes

		return nbytes, nil
	case storage.StorageResourcePackfile:
		if s.mode&storage.ModeWrite == 0 {
			return 0, storage.ErrNotWritable
		}

		s.packfileOffset = s.configOffset + s.configLength
		nbytes, err := io.Copy(s.fp, rd)
		if err != nil {
			return 0, err
		}
		s.packfileLength = nbytes

		return nbytes, nil
	case storage.StorageResourceLock:
		if s.mode&storage.ModeWrite == 0 {
			return 0, storage.ErrNotWritable
		}
		return 0, nil
	}

	return -1, errors.ErrUnsupported
}

func (s *Store) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	switch res {
	case storage.StorageResourceState:
		if mac != stateMAC {
			return nil, fmt.Errorf("invalid MAC: %s", mac)
		}
		return io.NopCloser(io.NewSectionReader(s.fp, s.stateOffset, s.stateLength)), nil
	case storage.StorageResourcePackfile:
		if rg == nil {
			return io.NopCloser(io.NewSectionReader(s.fp, s.packfileOffset, s.packfileLength)), nil
		} else {
			return io.NopCloser(io.NewSectionReader(s.fp, s.packfileOffset+int64(rg.Offset), int64(rg.Length))), nil
		}
	case storage.StorageResourceLock:
		return io.NopCloser(bytes.NewBuffer([]byte{})), nil
	}

	return nil, errors.ErrUnsupported
}

func (s *Store) Delete(context.Context, storage.StorageResource, objects.MAC) error {
	if s.mode&storage.ModeWrite == 0 {
		return storage.ErrNotWritable
	}
	return nil
}
