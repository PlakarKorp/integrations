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

package sftp

// General pattern:
//
//	ts := newTestServer(t)
//	s := ts.newTestSftp(t, "/repo")
//	// ... call s.Create(ctx, ...) / s.Open(ctx) / s.List/Get/Put/Delete ...
//	// ... cross-check with expected value

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoragePath(t *testing.T) {
	tests := []struct {
		name    string
		rootDir string
		args    []string
		want    string
	}{
		{
			name:    "single component under nested root",
			rootDir: "/fakepath",
			args:    []string{"a", "b"},
			want:    "/fakepath/a/b",
		},
		{
			name:    "root directory",
			rootDir: "/",
			args:    []string{"a", "b"},
			want:    "/a/b",
		},
		{
			name:    "no extra components",
			rootDir: "/fakepath",
			args:    nil,
			want:    "/fakepath",
		},
		{
			name:    "single component",
			rootDir: "/fakepath",
			args:    []string{"config"},
			want:    "/fakepath/config",
		},
		{
			name:    "nested rootDir with trailing slash is cleaned",
			rootDir: "/fakepath/",
			args:    []string{"a", "b"},
			want:    "/fakepath/a/b",
		},
		{
			name:    "use dot notations",
			rootDir: "/fakepath/subdir",
			args:    []string{"a", "b"},
			want:    "/fakepath/subdir/a/b",
		},
		{
			name:    "use dot notations",
			rootDir: "/fakepath/",
			args:    []string{"..", "a", "b"},
			want:    "/a/b", // This is path related issue for which we will add a fix later.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t)
			s := ts.newTestSftp(t, tt.rootDir)
			defer s.Close(context.Background())

			got := s.path(tt.args...)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestStorageCreate_FreshRepo(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	// Check that the repo directory was created with the correct permissions.
	info, err := ts.realStat("/repo")
	require.NoError(t, err)
	require.True(t, info.IsDir(), "expected /repo to be a directory, got: %v", info.Mode())
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())

	// Check that the packfiles and states directories were created.
	_, err = ts.realStat("/repo/packfiles")
	require.NoError(t, err)
	_, err = ts.realStat("/repo/states")
	require.NoError(t, err)
	// Check that the locks directory was created.
	_, err = ts.realStat("/repo/locks")
	require.NoError(t, err)
}

func TestStorageCreate_NonEmptyRepo(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	// Create a non-empty directory at /repo.
	require.NoError(t, os.MkdirAll(ts.realPath("/repo"), 0700))
	require.NoError(t, os.WriteFile(ts.realPath("/repo/somefile"), []byte("data"), 0600))

	err := s.Create(context.Background(), []byte("test config"))
	require.Error(t, err, "s.Create() initialised a non empty directory")
}

func TestStorageOpen(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close((context.Background()))

	s.Create(context.Background(), []byte("test config"))
	_, err := s.Open(context.Background())

	require.NoError(t, err, "failed to open CONFIG file")
}

func TestStorageOpen_MissingConfig(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close((context.Background()))

	_, err := s.Open(context.Background())

	require.Error(t, err, "expected error when opening missing CONFIG file")
}

func TestStorageMetadata(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close((context.Background()))

	mode, err := s.Mode(context.Background())
	require.NoError(t, err)
	want := storage.ModeRead | storage.ModeWrite
	require.Equal(t, want, mode)

	size, err := s.Size(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(-1), size)
}

func TestStorageList_Supported(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close((context.Background()))

	require.NoError(t, s.Create(context.Background(), []byte("test config")))
	supportedStorageFiletypes := []storage.StorageResource{
		storage.StorageResourcePackfile,
		storage.StorageResourceLock,
		storage.StorageResourceState,
	}
	for _, tt := range supportedStorageFiletypes {
		t.Run(tt.String(), func(t *testing.T) {
			got, err := s.List(context.Background(), tt)
			require.NoError(t, err)
			// Fresh repo: nothing has been Put() for this resource yet.
			require.Empty(t, got)

			m := mac(0x33)
			_, err = s.Put(context.Background(), tt, m, bytes.NewReader([]byte("data")))
			require.NoError(t, err)

			got, err = s.List(context.Background(), tt)
			require.NoError(t, err)
			require.Equal(t, []objects.MAC{m}, got)
		})
	}
}

func TestStorageList_UnsupportedResource(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close((context.Background()))

	s.Create(context.Background(), []byte("test config"))
	unsupportedStorageFiletypes := []storage.StorageResource{
		storage.StorageResourceUndefined,
		storage.StorageResourceECCPackfile,
		storage.StorageResourceECCState,
	}
	for _, tt := range unsupportedStorageFiletypes {
		t.Run(tt.String(), func(t *testing.T) {
			_, err := s.List(context.Background(), tt)
			require.Error(t, err, "expected error when accessing s.List() for %s", tt.String())
		})
	}
}

func TestStorageGetPut_Packfile(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), storage.StorageResourcePackfile, m, bytes.NewReader(want))
	require.NoError(t, err)

	rc, err := s.Get(context.Background(), storage.StorageResourcePackfile, m, nil)
	require.NoError(t, err)
	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestStorageGetPut_State(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), storage.StorageResourceState, m, bytes.NewReader(want))
	require.NoError(t, err)

	rc, err := s.Get(context.Background(), storage.StorageResourceState, m, nil)
	require.NoError(t, err)
	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestStorageGet_PackfileRange(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), storage.StorageResourcePackfile, m, bytes.NewReader(want))
	require.NoError(t, err)

	rc, err := s.Get(context.Background(), storage.StorageResourcePackfile, m, &storage.Range{Offset: 5, Length: 8})
	require.NoError(t, err)
	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, string(want)[5:5+8], string(got))
}

func TestStorageGet_StateRangeUnsupported(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), storage.StorageResourceState, m, bytes.NewReader(want))
	require.NoError(t, err)

	_, err = s.Get(context.Background(), storage.StorageResourceState, m, &storage.Range{Offset: 5, Length: 8})
	require.Error(t, err, "s.Get() expected to fail for state range request")
}

func TestStorageGetPut_Lock(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), storage.StorageResourceLock, m, bytes.NewReader(want))
	require.NoError(t, err)

	rc, err := s.Get(context.Background(), storage.StorageResourceLock, m, nil)
	require.NoError(t, err)
	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestStorageGet_LockRangeUnsupported(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), storage.StorageResourceLock, m, bytes.NewReader(want))
	require.NoError(t, err)

	_, err = s.Get(context.Background(), storage.StorageResourceLock, m, &storage.Range{Offset: 5, Length: 8})
	require.Error(t, err, "s.Get() expected to fail for lock range request")
}

func TestStorageDelete_Packfile(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	m := mac(0x2a)
	want := []byte("test packfile data")
	_, err := s.Put(context.Background(), storage.StorageResourcePackfile, m, bytes.NewReader(want))
	require.NoError(t, err)

	require.NoError(t, s.Delete(context.Background(), storage.StorageResourcePackfile, m))

	_, err = s.Get(context.Background(), storage.StorageResourcePackfile, m, nil)
	require.Error(t, err, "s.Get() succeeded after Delete(), want error")
}

func TestStorageDelete_State(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	m := mac(0x2a)
	want := []byte("test packfile data")
	_, err := s.Put(context.Background(), storage.StorageResourceState, m, bytes.NewReader(want))
	require.NoError(t, err)

	require.NoError(t, s.Delete(context.Background(), storage.StorageResourceState, m))

	_, err = s.Get(context.Background(), storage.StorageResourceState, m, nil)
	require.Error(t, err, "s.Get() succeeded after Delete(), want error")
}

func TestStorageDelete_LockNonExistent(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	m := mac(0x2a)

	err := s.Delete(context.Background(), storage.StorageResourceLock, m)
	require.Error(t, err, "s.Put() expected a failure")
}

func TestStorageDelete_UnsupportedResource(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	m := mac(0x2a)

	err := s.Delete(context.Background(), storage.StorageResourceUndefined, m)
	require.Error(t, err, "s.Put() expected a failure")
}

func TestStorageGetLocks_IgnoresBadNames(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	// Two valid locks, put through the real API.
	want := map[objects.MAC]bool{
		mac(0x11): true,
		mac(0x22): true,
	}
	for m := range want {
		_, err := s.Put(context.Background(), storage.StorageResourceLock, m, bytes.NewReader([]byte("data")))
		require.NoError(t, err)
	}

	// Bad entries written directly to disk, bypassing the Sftp API.
	require.NoError(t, os.WriteFile(ts.realPath("/repo/locks/not-a-hexname"), []byte("x"), 0600))
	require.NoError(t, os.WriteFile(ts.realPath("/repo/locks/deadbeef"), []byte("x"), 0600))

	got, err := s.List(context.Background(), storage.StorageResourceLock)
	require.NoError(t, err)

	require.ElementsMatch(t, keys(want), got)
}

// TestStorageConcurrentPacksAndStates exercises concurrent Put/List across
// both packfiles and states buckets, run under -race to catch any data race
// in buckets.List()'s mutex-guarded append and to confirm every concurrently
// written entry is observed exactly once.
func TestStorageConcurrentPacksAndStates(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	const n = 32
	wantPackfiles := make(map[objects.MAC]bool, n)
	wantStates := make(map[objects.MAC]bool, n)
	for i := range n {
		var m objects.MAC
		m[0] = byte(i)
		m[1] = 0xaa
		wantPackfiles[m] = true

		var sm objects.MAC
		sm[0] = byte(i)
		sm[1] = 0xbb
		wantStates[sm] = true
	}

	var wg sync.WaitGroup
	put := func(res storage.StorageResource, m objects.MAC) {
		defer wg.Done()
		_, err := s.Put(context.Background(), res, m, bytes.NewReader([]byte("data")))
		assert.NoError(t, err, "s.Put(%s, %x) failed", res.String(), m)
	}
	for m := range wantPackfiles {
		wg.Add(1)
		go put(storage.StorageResourcePackfile, m)
	}
	for m := range wantStates {
		wg.Add(1)
		go put(storage.StorageResourceState, m)
	}
	wg.Wait()

	gotPackfiles, err := s.List(context.Background(), storage.StorageResourcePackfile)
	require.NoError(t, err)
	require.ElementsMatch(t, keys(wantPackfiles), gotPackfiles)

	gotStates, err := s.List(context.Background(), storage.StorageResourceState)
	require.NoError(t, err)
	require.ElementsMatch(t, keys(wantStates), gotStates)
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

// mac builds a deterministic objects.MAC for use in table-driven tests,
// with the first byte set to b (useful for landing entries in a specific
// bucket, since buckets are keyed off mac[0]).
func mac(b byte) objects.MAC {
	var m objects.MAC
	m[0] = b
	return m
}

// keys returns the keys of a map[objects.MAC]bool as a slice, for use with
// require.ElementsMatch (order-independent comparison against a []objects.MAC
// result, e.g. from s.List()).
func keys(m map[objects.MAC]bool) []objects.MAC {
	ret := make([]objects.MAC, 0, len(m))
	for k := range m {
		ret = append(ret, k)
	}
	return ret
}
