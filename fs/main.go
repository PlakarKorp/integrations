package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/PlakarKorp/go-kloset-sdk/sdk"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/PlakarKorp/kloset/storage"
)


type Storage struct {
	location  string
	packfiles Buckets
	states    Buckets
}

func NewStore(ctx context.Context, proto string, storeConfig map[string]string) (storage.Store, error) {
	return &Storage{
		location: storeConfig["location"],
	}, nil
}

func (s *Storage) Location() string {
	return s.location
}

func (s *Storage) Path(args ...string) string {
	root := strings.TrimPrefix(s.Location(), "fis://")

	args = append(args, "")
	copy(args[1:], args)
	args[0] = root

	return filepath.Join(args...)
}
func (s *Storage) Create(ctx context.Context, config []byte) error {
	dirfp, err := os.Open(s.Path())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		err = os.MkdirAll(s.Path(), 0700)
		if err != nil {
			return err
		}
	} else {
		defer dirfp.Close()
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

func (s *Storage) Open(ctx context.Context) ([]byte, error) {
	s.packfiles = NewBuckets(s.Path("packfiles"))
	s.states = NewBuckets(s.Path("states"))

	rd, err := os.Open(s.Path("CONFIG"))
	if err != nil {
		return nil, err
	}
	defer rd.Close() // do we care about err?

	data, err := io.ReadAll(rd)
	if err != nil {
		return nil, err
	}

	return data, nil
}


func (s *Storage) Mode() storage.Mode {
	return storage.ModeRead | storage.ModeWrite
}

func (s *Storage) Size() int64 {
	var size int64
	location := strings.TrimPrefix(s.Location(), "fis://")
	_ = filepath.WalkDir(location, func(_ string, d fs.DirEntry, err error) error {
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
	return size
}

func (s *Storage) GetPackfiles() ([]objects.MAC, error) {
	return s.packfiles.List()
}

func (s *Storage) GetPackfile(mac objects.MAC) (io.Reader, error) {
	fp, err := s.packfiles.Get(mac)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = repository.ErrPackfileNotFound
		}
		return nil, err
	}

	return fp, nil
}

func (s *Storage) GetPackfileBlob(mac objects.MAC, offset uint64, length uint32) (io.Reader, error) {
	res, err := s.packfiles.GetBlob(mac, offset, length)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = repository.ErrPackfileNotFound
		}
		return nil, err
	}
	return res, nil
}

func (s *Storage) DeletePackfile(mac objects.MAC) error {
	return s.packfiles.Remove(mac)
}

func (s *Storage) PutPackfile(mac objects.MAC, rd io.Reader) (int64, error) {
	return s.packfiles.Put(mac, rd)
}

func (s *Storage) Close() error {
	return nil
}

/* Indexes */
func (s *Storage) GetStates() ([]objects.MAC, error) {
	return s.states.List()
}

func (s *Storage) PutState(mac objects.MAC, rd io.Reader) (int64, error) {
	return s.states.Put(mac, rd)
}

func (s *Storage) GetState(mac objects.MAC) (io.Reader, error) {
	return s.states.Get(mac)
}

func (s *Storage) DeleteState(mac objects.MAC) error {
	return s.states.Remove(mac)
}

func (s *Storage) GetLocks() ([]objects.MAC, error) {
	ret := make([]objects.MAC, 0)

	locksdir, err := os.ReadDir(s.Path("locks"))
	if err != nil {
		return nil, err
	}

	for _, lock := range locksdir {
		if !lock.Type().IsRegular() {
			continue
		}

		lockID, err := hex.DecodeString(lock.Name())
		if err != nil {
			return nil, err
		}

		if len(lockID) != 32 {
			continue
		}

		ret = append(ret, objects.MAC(lockID))
	}

	return ret, nil
}

func (s *Storage) PutLock(lockID objects.MAC, rd io.Reader) (int64, error) {
	return WriteToFileAtomicTempDir(filepath.Join(s.Path("locks"), hex.EncodeToString(lockID[:])), rd, s.Path(""))
}

func (s *Storage) GetLock(lockID objects.MAC) (io.Reader, error) {
	fp, err := os.Open(filepath.Join(s.Path("locks"), hex.EncodeToString(lockID[:])))
	if err != nil {
		return nil, err
	}

	return ClosingReader(fp)
}

func (s *Storage) DeleteLock(lockID objects.MAC) error {
	if err := os.Remove(filepath.Join(s.Path("locks"), hex.EncodeToString(lockID[:]))); err != nil {
		return err
	}

	return nil
}


func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s <scan-dir>\n", os.Args[0])
		os.Exit(1)
	}

	argStr := os.Args[1]
	argStr = strings.TrimPrefix(argStr, "map[")
	argStr = strings.TrimSuffix(argStr, "]")
	scanMap := make(map[string]string)
	for _, pair := range strings.Fields(argStr) {
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) == 2 {
			scanMap[kv[0]] = kv[1]
		}
	}

	fsStorage, err := NewStore(context.Background(), "fis", scanMap)
	if err != nil {
		panic(err)
	}

	if err := sdk.RunStorage(fsStorage); err != nil {
		panic(err)
	}
}
