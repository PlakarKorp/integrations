package storage

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/require"
)

// newV0Database writes a database with the schema Create produced before
// migrations existed, in WAL mode like every database Create made, holding
// one row per resource.
func newV0Database(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repo.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE configuration (value BLOB)`,
		`CREATE TABLE states (mac VARCHAR(64) NOT NULL PRIMARY KEY, data BLOB)`,
		`CREATE TABLE packfiles (mac VARCHAR(64) NOT NULL PRIMARY KEY, data BLOB)`,
		`CREATE TABLE locks (mac VARCHAR(64) NOT NULL PRIMARY KEY, data BLOB)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO configuration (value) VALUES (?)`, []byte("config"))
	require.NoError(t, err)
	old := integration.MAC{0x01}
	_, err = db.Exec(`INSERT INTO states (mac, data) VALUES (?, ?)`, old[:], []byte("old"))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO packfiles (mac, data) VALUES (?, ?)`, old[:], []byte("old"))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO locks (mac, data) VALUES (?, ?)`, old.FormatHex(), []byte("old"))
	require.NoError(t, err)
	return path
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var v int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&v))
	return v
}

func openStore(t *testing.T, path string) integration.Store {
	t.Helper()
	st, err := NewStore(t.Context(), "sqlite", map[string]string{"location": "sqlite://" + path})
	require.NoError(t, err)
	config, err := st.Open(t.Context())
	require.NoError(t, err)
	require.Equal(t, []byte("config"), config)
	t.Cleanup(func() { _ = st.Close(t.Context()) })
	return st
}

func listMTimes(t *testing.T, st integration.Store, res integration.StorageResource) map[integration.MAC]time.Time {
	t.Helper()
	got := map[integration.MAC]time.Time{}
	for e, err := range st.List(t.Context(), res, 0) {
		require.NoError(t, err)
		got[e.MAC] = e.MTime
	}
	return got
}

func TestCreateFillsMTime(t *testing.T) {
	for _, tt := range listResources {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now()
			st, macs := newListStore(t, tt.res, 3)
			after := time.Now()

			got := listMTimes(t, st, tt.res)
			require.Len(t, got, len(macs))
			for _, mac := range macs {
				mtime := got[mac]
				require.False(t, mtime.Before(before), "mtime %v before %v", mtime, before)
				require.False(t, mtime.After(after), "mtime %v after %v", mtime, after)
			}
		})
	}
}

func TestOpenMigratesV0Database(t *testing.T) {
	path := newV0Database(t)
	require.Equal(t, 0, userVersion(t, path))

	st := openStore(t, path)
	require.Equal(t, schemaVersion, userVersion(t, path))

	old, fresh := integration.MAC{0x01}, integration.MAC{0x02}
	for _, tt := range listResources {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now()
			_, err := st.Put(t.Context(), tt.res, 0, fresh, bytes.NewReader([]byte("new")))
			require.NoError(t, err)

			got := listMTimes(t, st, tt.res)
			require.Len(t, got, 2)
			require.True(t, got[old].IsZero(), "row from before the migration has mtime %v", got[old])
			require.False(t, got[fresh].Before(before))

			rd, err := st.Get(t.Context(), tt.res, 0, old, nil)
			require.NoError(t, err)
			var buf bytes.Buffer
			_, err = buf.ReadFrom(rd)
			require.NoError(t, err)
			require.NoError(t, rd.Close())
			require.Equal(t, "old", buf.String())
		})
	}
}

func TestOpenMigratedDatabaseAgain(t *testing.T) {
	path := newV0Database(t)
	openStore(t, path)
	openStore(t, path)
	require.Equal(t, schemaVersion, userVersion(t, path))
}

func TestOpenUpToDateDatabaseWhileWriting(t *testing.T) {
	path := newV0Database(t)
	openStore(t, path)

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = conn.ExecContext(t.Context(), "ROLLBACK") })

	openStore(t, path)
}

func TestConcurrentOpensMigrateOnce(t *testing.T) {
	path := newV0Database(t)

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			st, err := NewStore(t.Context(), "sqlite", map[string]string{"location": "sqlite://" + path})
			if err != nil {
				errs <- err
				return
			}
			if _, err := st.Open(t.Context()); err != nil {
				errs <- err
				return
			}
			errs <- st.Close(t.Context())
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, schemaVersion, userVersion(t, path))
}

func TestLockRefreshUpdatesMTime(t *testing.T) {
	path := newV0Database(t)
	st := openStore(t, path)

	mac := integration.MAC{0x42}
	_, err := st.Put(t.Context(), integration.StorageResourceLock, 0, mac, bytes.NewReader([]byte("v1")))
	require.NoError(t, err)

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`UPDATE locks SET mtime = 1 WHERE mac = ?`, mac.FormatHex())
	require.NoError(t, err)
	require.Equal(t, time.Unix(0, 1), listMTimes(t, st, integration.StorageResourceLock)[mac])

	before := time.Now()
	_, err = st.Put(t.Context(), integration.StorageResourceLock, 0, mac, bytes.NewReader([]byte("v2")))
	require.NoError(t, err)
	require.False(t, listMTimes(t, st, integration.StorageResourceLock)[mac].Before(before))
}
