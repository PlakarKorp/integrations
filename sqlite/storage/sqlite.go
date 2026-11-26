/*
 * Copyright (c) 2021 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
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
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
	_ "modernc.org/sqlite"
)

type Store struct {
	backend string

	conn    *sql.DB
	wrMutex sync.Mutex

	Repository string
	location   string
}

var ErrBlobNotFound = errors.New("blob not found")

func init() {
	_ = integration.RegisterStore("sqlite", location.FLAG_LOCALFS|location.FLAG_FILE, NewStore)
}

func NewStore(ctx context.Context, proto string, storeConfig map[string]string) (integration.Store, error) {
	if proto != "sqlite" {
		return nil, fmt.Errorf("unsupported database backend: %s", proto)
	}

	location := storeConfig["location"]
	return &Store{
		backend:  proto,
		location: location,
	}, nil
}

func (s *Store) Location(context context.Context) (string, error) {
	return s.location, nil
}

func (s *Store) connect(addr string) error {
	conn, err := sql.Open(s.backend, addr)
	if err != nil {
		return err
	}

	if s.backend == "sqlite" {
		// busy_timeout first, so switching to WAL waits on concurrent
		// openers instead of failing with SQLITE_BUSY.
		if _, err := conn.Exec("PRAGMA busy_timeout=2000;"); err != nil {
			_ = conn.Close()
			return fmt.Errorf("setting busy_timeout: %w", err)
		}
		if _, err := conn.Exec("PRAGMA journal_mode=WAL;"); err != nil {
			_ = conn.Close()
			return fmt.Errorf("setting journal_mode=WAL: %w", err)
		}
	}

	s.conn = conn
	return nil
}

func (s *Store) Create(ctx context.Context, config []byte) error {
	location := strings.TrimPrefix(s.location, "sqlite://")

	// sqlite creates the -wal and -shm files with the mode of the database
	fp, err := os.OpenFile(location, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := fp.Close(); err != nil {
		return err
	}

	err = s.connect(location)
	if err != nil {
		return err
	}

	statement, err := s.conn.Prepare(`CREATE TABLE IF NOT EXISTS configuration (
		value	BLOB
	);`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	if _, err := statement.Exec(); err != nil {
		return err
	}

	statement, err = s.conn.Prepare(`CREATE TABLE IF NOT EXISTS states (
		mac	VARCHAR(64) NOT NULL PRIMARY KEY,
		data		BLOB
	);`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	if _, err := statement.Exec(); err != nil {
		return err
	}

	statement, err = s.conn.Prepare(`CREATE TABLE IF NOT EXISTS packfiles (
		mac	VARCHAR(64) NOT NULL PRIMARY KEY,
		data		BLOB
	);`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	if _, err := statement.Exec(); err != nil {
		return err
	}

	statement, err = s.conn.Prepare(`CREATE TABLE IF NOT EXISTS locks (
		mac	VARCHAR(64) NOT NULL PRIMARY KEY,
		data		BLOB
	);`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	if _, err := statement.Exec(); err != nil {
		return err
	}

	if err := s.migrate(ctx); err != nil {
		return err
	}

	statement, err = s.conn.Prepare(`INSERT INTO configuration(value) VALUES(?)`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()

	_, err = statement.Exec(config)
	if err != nil {
		return err
	}

	return nil
}

func (s *Store) Open(ctx context.Context) ([]byte, error) {
	location := strings.TrimPrefix(s.location, "sqlite://")
	err := s.connect(location)
	if err != nil {
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}

	var buffer []byte

	err = s.conn.QueryRow(`SELECT value FROM configuration`).Scan(&buffer)
	if err != nil {
		return nil, err
	}
	return buffer, nil
}

// migrations upgrade the schema one step at a time, and user_version
// records how many have been applied. Version 0 is the schema created
// before migrations existed.
var migrations = [...][]string{
	// 1: modification time in Unix nanoseconds, NULL for older rows.
	{
		`ALTER TABLE packfiles ADD COLUMN mtime INTEGER`,
		`ALTER TABLE states ADD COLUMN mtime INTEGER`,
		`ALTER TABLE locks ADD COLUMN mtime INTEGER`,
	},
}

const schemaVersion = len(migrations)

// migrate applies the missing migrations. BEGIN IMMEDIATE takes the write
// lock before reading user_version, so concurrent opens of an old database
// apply each migration once.
func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= schemaVersion {
		return nil
	}

	conn, err := s.conn.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	// busy_timeout is per connection and this one may be new.
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=2000"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i := version; i < schemaVersion; i++ {
		for _, stmt := range migrations[i] {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration %d: %w", i+1, err)
			}
		}
	}
	if version < schemaVersion {
		// PRAGMA does not take bound parameters.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
			return err
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *Store) Origin() string        { return "localhost" }
func (s *Store) Root() string          { return s.location }
func (s *Store) Type() string          { return "sqlite" }
func (s *Store) Flags() location.Flags { return 0 }

func (s *Store) Mode(context context.Context) (integration.Mode, error) {
	return integration.ModeRead | integration.ModeWrite, nil
}

func (s *Store) Size(context context.Context) (int64, error) {
	return -1, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return nil
}

func (s *Store) Close(ctx context.Context) error {
	return s.conn.Close()
}

func (s *Store) List(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag) iter.Seq2[*integration.StorageResourceEntry, error] {
	switch res {
	case integration.StorageResourcePackfile:
		return s.getPackfiles()
	case integration.StorageResourceState:
		return s.getStates()
	case integration.StorageResourceLock:
		return s.getLocks()
	default:
		return func(yield func(*integration.StorageResourceEntry, error) bool) {
			yield(nil, errors.ErrUnsupported)
		}
	}
}

func (s *Store) Put(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag, mac integration.MAC, rd io.Reader) (int64, error) {
	switch res {
	case integration.StorageResourcePackfile:
		return s.putPackfile(mac, rd)
	case integration.StorageResourceState:
		return s.putState(mac, rd)
	case integration.StorageResourceLock:
		return s.putLock(mac, rd)
	default:
		return -1, errors.ErrUnsupported
	}
}

func (s *Store) Get(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag, mac integration.MAC, rg *integration.Range) (io.ReadCloser, error) {
	switch res {
	case integration.StorageResourcePackfile:
		if rg != nil {
			return s.getPackfileBlob(mac, rg.Offset, rg.Length)
		}
		return s.getPackfile(mac)
	case integration.StorageResourceState:
		if rg != nil {
			return nil, errors.ErrUnsupported
		}
		return s.getState(mac)
	case integration.StorageResourceLock:
		if rg != nil {
			return nil, errors.ErrUnsupported
		}
		return s.getLock(mac)
	default:
		return nil, errors.ErrUnsupported
	}
}

func (s *Store) Delete(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag, mac integration.MAC) error {
	switch res {
	case integration.StorageResourcePackfile:
		return s.deletePackfile(mac)
	case integration.StorageResourceState:
		return s.deleteState(mac)
	case integration.StorageResourceLock:
		return s.deleteLock(mac)
	default:
		return errors.ErrUnsupported
	}
}

// states
func (s *Store) getStates() iter.Seq2[*integration.StorageResourceEntry, error] {
	return func(yield func(*integration.StorageResourceEntry, error) bool) {
		rows, err := s.conn.Query("SELECT mac, IFNULL(length(data), 0), mtime FROM states")
		if err != nil {
			yield(nil, err)
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var mac []byte
			var mtime sql.NullInt64
			var entry integration.StorageResourceEntry
			err = rows.Scan(&mac, &entry.Size, &mtime)
			if err != nil {
				yield(nil, err)
				return
			}
			if len(mac) != len(entry.MAC) {
				yield(nil, fmt.Errorf("invalid mac length %d in states", len(mac)))
				return
			}
			copy(entry.MAC[:], mac)
			if mtime.Valid {
				entry.MTime = time.Unix(0, mtime.Int64)
			}
			if !yield(&entry, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, err)
		}
	}
}

func (s *Store) putState(mac integration.MAC, rd io.Reader) (int64, error) {
	data, err := io.ReadAll(rd)
	if err != nil {
		return 0, err
	}

	statement, err := s.conn.Prepare(`INSERT INTO states (mac, data, mtime) VALUES(?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = statement.Close() }()

	s.wrMutex.Lock()
	_, err = statement.Exec(mac[:], data, time.Now().UnixNano())
	s.wrMutex.Unlock()
	if err != nil {
		return 0, err
	}

	return int64(len(data)), nil
}

func (s *Store) getState(mac integration.MAC) (io.ReadCloser, error) {
	var data []byte
	err := s.conn.QueryRow(`SELECT data FROM states WHERE mac=?`, mac[:]).Scan(&data)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewBuffer(data)), nil
}

func (s *Store) deleteState(mac integration.MAC) error {
	statement, err := s.conn.Prepare(`DELETE FROM states WHERE mac=?`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()

	s.wrMutex.Lock()
	_, err = statement.Exec(mac[:])
	s.wrMutex.Unlock()
	if err != nil {
		// if err is that it's already present, we should discard err and assume a concurrent write
		return err
	}
	return nil
}

// packfiles
func (s *Store) getPackfiles() iter.Seq2[*integration.StorageResourceEntry, error] {
	return func(yield func(*integration.StorageResourceEntry, error) bool) {
		rows, err := s.conn.Query("SELECT mac, IFNULL(length(data), 0), mtime FROM packfiles")
		if err != nil {
			yield(nil, err)
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var mac []byte
			var mtime sql.NullInt64
			var entry integration.StorageResourceEntry
			err = rows.Scan(&mac, &entry.Size, &mtime)
			if err != nil {
				yield(nil, err)
				return
			}
			if len(mac) != len(entry.MAC) {
				yield(nil, fmt.Errorf("invalid mac length %d in packfiles", len(mac)))
				return
			}
			copy(entry.MAC[:], mac)
			if mtime.Valid {
				entry.MTime = time.Unix(0, mtime.Int64)
			}
			if !yield(&entry, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, err)
		}
	}
}

func (s *Store) putPackfile(mac integration.MAC, rd io.Reader) (int64, error) {
	data, err := io.ReadAll(rd)
	if err != nil {
		return 0, err
	}

	statement, err := s.conn.Prepare(`INSERT INTO packfiles (mac, data, mtime) VALUES(?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = statement.Close() }()

	s.wrMutex.Lock()
	_, err = statement.Exec(mac[:], data, time.Now().UnixNano())
	s.wrMutex.Unlock()
	if err != nil {
		return 0, err
	}

	return int64(len(data)), nil
}

func (s *Store) getPackfile(mac integration.MAC) (io.ReadCloser, error) {
	var data []byte
	err := s.conn.QueryRow(`SELECT data FROM packfiles WHERE mac=?`, mac[:]).Scan(&data)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *Store) getPackfileBlob(mac integration.MAC, offset uint64, length uint32) (io.ReadCloser, error) {
	var data []byte
	err := s.conn.QueryRow(`SELECT substr(data, ?, ?) FROM packfiles WHERE mac=?`, offset+1, length, mac[:]).Scan(&data)
	if err != nil {
		if err == sql.ErrNoRows {
			err = ErrBlobNotFound
		}
		return nil, err
	}
	return io.NopCloser(bytes.NewBuffer(data)), nil
}

func (s *Store) deletePackfile(mac integration.MAC) error {
	statement, err := s.conn.Prepare(`DELETE FROM packfiles WHERE mac=?`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()

	s.wrMutex.Lock()
	_, err = statement.Exec(mac[:])
	s.wrMutex.Unlock()
	if err != nil {
		// if err is that it's already present, we should discard err and assume a concurrent write
		return err
	}
	return nil
}

func (s *Store) getLocks() iter.Seq2[*integration.StorageResourceEntry, error] {
	return func(yield func(*integration.StorageResourceEntry, error) bool) {
		rows, err := s.conn.Query("SELECT mac, IFNULL(length(data), 0), mtime FROM locks")
		if err != nil {
			yield(nil, err)
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var mac string
			var size int64
			var mtime sql.NullInt64
			err = rows.Scan(&mac, &size, &mtime)
			if err != nil {
				yield(nil, err)
				return
			}

			lockID, err := hex.DecodeString(mac)
			if err != nil {
				yield(nil, err)
				return
			}

			if len(lockID) != 32 {
				continue
			}

			entry := integration.StorageResourceEntry{MAC: integration.MAC(lockID), Size: size}
			if mtime.Valid {
				entry.MTime = time.Unix(0, mtime.Int64)
			}
			if !yield(&entry, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, err)
		}
	}
}

func (s *Store) putLock(lockID integration.MAC, rd io.Reader) (int64, error) {
	data, err := io.ReadAll(rd)
	if err != nil {
		return 0, err
	}

	statement, err := s.conn.Prepare(`INSERT INTO locks (mac, data, mtime) VALUES(?, ?, ?) ON CONFLICT(mac) DO UPDATE SET data=excluded.data, mtime=excluded.mtime`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = statement.Close() }()

	s.wrMutex.Lock()
	_, err = statement.Exec(hex.EncodeToString(lockID[:]), data, time.Now().UnixNano())
	s.wrMutex.Unlock()
	if err != nil {
		return 0, err
	}

	return int64(len(data)), nil
}

func (s *Store) getLock(lockID integration.MAC) (io.ReadCloser, error) {
	var data []byte
	err := s.conn.QueryRow(`SELECT data FROM locks WHERE mac=?`, hex.EncodeToString(lockID[:])).Scan(&data)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewBuffer(data)), nil
}

func (s *Store) deleteLock(lockID integration.MAC) error {
	statement, err := s.conn.Prepare(`DELETE FROM locks WHERE mac=?`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()

	s.wrMutex.Lock()
	_, err = statement.Exec(hex.EncodeToString(lockID[:]))
	s.wrMutex.Unlock()
	if err != nil {
		// if err is that it's already present, we should discard err and assume a concurrent write
		return err
	}
	return nil
}
