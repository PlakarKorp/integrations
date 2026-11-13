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
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
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
			defer func() { _ = s.Close(context.Background()) }()

			got := s.path(tt.args...)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestStorageCreate_FreshRepo(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

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
	defer func() { _ = s.Close(context.Background()) }()

	// Create a non-empty directory at /repo.
	require.NoError(t, os.MkdirAll(ts.realPath("/repo"), 0700))
	require.NoError(t, os.WriteFile(ts.realPath("/repo/somefile"), []byte("data"), 0600))

	err := s.Create(context.Background(), []byte("test config"))
	require.Error(t, err, "s.Create() initialised a non empty directory")
}

func TestStorageOpen(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))
	_, err := s.Open(context.Background())

	require.NoError(t, err, "failed to open CONFIG file")
}

func TestStorageOpen_MissingConfig(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	_, err := s.Open(context.Background())

	require.Error(t, err, "expected error when opening missing CONFIG file")
}

func TestStorageMetadata(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	mode, err := s.Mode(context.Background())
	require.NoError(t, err)
	want := integration.ModeRead | integration.ModeWrite
	require.Equal(t, want, mode)

	size, err := s.Size(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(-1), size)
}

func TestStorageList_Supported(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))
	supportedStorageFiletypes := []integration.StorageResource{
		integration.StorageResourcePackfile,
		integration.StorageResourceLock,
		integration.StorageResourceState,
	}
	for _, tt := range supportedStorageFiletypes {
		t.Run(tt.String(), func(t *testing.T) {
			got, err := list(s, tt)
			require.NoError(t, err)
			// Fresh repo: nothing has been Put() for this resource yet.
			require.Empty(t, got)

			m := mac(0x33)
			_, err = s.Put(context.Background(), tt, 0, m, bytes.NewReader([]byte("data")))
			require.NoError(t, err)

			got, err = list(s, tt)
			require.NoError(t, err)
			require.Equal(t, []integration.MAC{m}, got)
		})
	}
}

func TestStorageList_UnsupportedResource(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))
	unsupportedStorageFiletypes := []integration.StorageResource{
		integration.StorageResourceUndefined,
		integration.StorageResourceECCPackfile,
		integration.StorageResourceECCState,
	}
	for _, tt := range unsupportedStorageFiletypes {
		t.Run(tt.String(), func(t *testing.T) {
			_, err := list(s, tt)
			require.Error(t, err, "expected error when accessing s.List() for %s", tt.String())
		})
	}
}

func TestStorageGetPut_Packfile(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), integration.StorageResourcePackfile, 0, m, bytes.NewReader(want))
	require.NoError(t, err)

	rc, err := s.Get(context.Background(), integration.StorageResourcePackfile, 0, m, nil)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestStorageGetPut_State(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), integration.StorageResourceState, 0, m, bytes.NewReader(want))
	require.NoError(t, err)

	rc, err := s.Get(context.Background(), integration.StorageResourceState, 0, m, nil)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestStorageGet_PackfileRange(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), integration.StorageResourcePackfile, 0, m, bytes.NewReader(want))
	require.NoError(t, err)

	rc, err := s.Get(context.Background(), integration.StorageResourcePackfile, 0, m, &integration.Range{Offset: 5, Length: 8})
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, string(want)[5:5+8], string(got))
}

func TestStorageGet_StateRangeUnsupported(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), integration.StorageResourceState, 0, m, bytes.NewReader(want))
	require.NoError(t, err)

	_, err = s.Get(context.Background(), integration.StorageResourceState, 0, m, &integration.Range{Offset: 5, Length: 8})
	require.Error(t, err, "s.Get() expected to fail for state range request")
}

func TestStorageGetPut_Lock(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), integration.StorageResourceLock, 0, m, bytes.NewReader(want))
	require.NoError(t, err)

	rc, err := s.Get(context.Background(), integration.StorageResourceLock, 0, m, nil)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestStorageGet_LockRangeUnsupported(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	want := []byte("test packfile data")
	m := mac(0x2a)

	_, err := s.Put(context.Background(), integration.StorageResourceLock, 0, m, bytes.NewReader(want))
	require.NoError(t, err)

	_, err = s.Get(context.Background(), integration.StorageResourceLock, 0, m, &integration.Range{Offset: 5, Length: 8})
	require.Error(t, err, "s.Get() expected to fail for lock range request")
}

func TestStorageDelete_Packfile(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	m := mac(0x2a)
	want := []byte("test packfile data")
	_, err := s.Put(context.Background(), integration.StorageResourcePackfile, 0, m, bytes.NewReader(want))
	require.NoError(t, err)

	require.NoError(t, s.Delete(context.Background(), integration.StorageResourcePackfile, 0, m))

	_, err = s.Get(context.Background(), integration.StorageResourcePackfile, 0, m, nil)
	require.Error(t, err, "s.Get() succeeded after Delete(), want error")
}

func TestStorageDelete_State(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	m := mac(0x2a)
	want := []byte("test packfile data")
	_, err := s.Put(context.Background(), integration.StorageResourceState, 0, m, bytes.NewReader(want))
	require.NoError(t, err)

	require.NoError(t, s.Delete(context.Background(), integration.StorageResourceState, 0, m))

	_, err = s.Get(context.Background(), integration.StorageResourceState, 0, m, nil)
	require.Error(t, err, "s.Get() succeeded after Delete(), want error")
}

func TestStorageDelete_LockNonExistent(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	m := mac(0x2a)

	err := s.Delete(context.Background(), integration.StorageResourceLock, 0, m)
	require.Error(t, err, "s.Put() expected a failure")
}

func TestStorageDelete_UnsupportedResource(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	m := mac(0x2a)

	err := s.Delete(context.Background(), integration.StorageResourceUndefined, 0, m)
	require.Error(t, err, "s.Put() expected a failure")
}

func TestStorageGetLocks_IgnoresBadNames(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	// Two valid locks, put through the real API.
	want := map[integration.MAC]bool{
		mac(0x11): true,
		mac(0x22): true,
	}
	for m := range want {
		_, err := s.Put(context.Background(), integration.StorageResourceLock, 0, m, bytes.NewReader([]byte("data")))
		require.NoError(t, err)
	}

	// Bad entries written directly to disk, bypassing the Sftp API.
	require.NoError(t, os.WriteFile(ts.realPath("/repo/locks/not-a-hexname"), []byte("x"), 0600))
	require.NoError(t, os.WriteFile(ts.realPath("/repo/locks/deadbeef"), []byte("x"), 0600))

	got, err := list(s, integration.StorageResourceLock)
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
	defer func() { _ = s.Close(context.Background()) }()

	require.NoError(t, s.Create(context.Background(), []byte("test config")))

	const n = 32
	wantPackfiles := make(map[integration.MAC]bool, n)
	wantStates := make(map[integration.MAC]bool, n)
	for i := range n {
		var m integration.MAC
		m[0] = byte(i)
		m[1] = 0xaa
		wantPackfiles[m] = true

		var sm integration.MAC
		sm[0] = byte(i)
		sm[1] = 0xbb
		wantStates[sm] = true
	}

	var wg sync.WaitGroup
	put := func(res integration.StorageResource, m integration.MAC) {
		defer wg.Done()
		_, err := s.Put(context.Background(), res, 0, m, bytes.NewReader([]byte("data")))
		assert.NoError(t, err, "s.Put(%s, %x) failed", res.String(), m)
	}
	for m := range wantPackfiles {
		wg.Add(1)
		go put(integration.StorageResourcePackfile, m)
	}
	for m := range wantStates {
		wg.Add(1)
		go put(integration.StorageResourceState, m)
	}
	wg.Wait()

	gotPackfiles, err := list(s, integration.StorageResourcePackfile)
	require.NoError(t, err)
	require.ElementsMatch(t, keys(wantPackfiles), gotPackfiles)

	gotStates, err := list(s, integration.StorageResourceState)
	require.NoError(t, err)
	require.ElementsMatch(t, keys(wantStates), gotStates)
}

func TestStorageBuckets_CreatePermissions(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")

	ctx := context.Background()
	err := s.Create(ctx, []byte(`sample`))
	require.NoError(t, err, "Create should succeed")

	// Verify root directory has restrictive permissions (0700).
	rootInfo, err := ts.client.Stat("/repo")
	require.NoError(t, err, "expected Stat to succeed for root directory")
	rootPerms := rootInfo.Mode().Perm()
	assert.Equal(t, os.FileMode(0700), rootPerms, "expected root directory to have 0700 permissions")

	// Verify all bucket subdirectories (00-ff) within packfiles have the
	// same restrictive permissions as the root (0700), not world-readable
	// permissions (0755). (Only checking packfiles buckets here, but states buckets should be the same.)
	for i := 0; i < 256; i++ {
		bucketName := fmt.Sprintf("%02x", i)
		bucketPath := "/repo/packfiles/" + bucketName
		bucketInfo, err := ts.client.Stat(bucketPath)
		require.NoError(t, err, "expected Stat to succeed for bucket directory packfiles/%s", bucketName)

		bucketPerms := bucketInfo.Mode().Perm()
		assert.Equal(t, os.FileMode(0700), bucketPerms,
			"expected bucket directory packfiles/%s to have 0700 permissions like the root, not 0755", bucketName)
	}
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

// mac builds a deterministic integration.MAC for use in table-driven tests,
// with the first byte set to b (useful for landing entries in a specific
// bucket, since buckets are keyed off mac[0]).
func mac(b byte) integration.MAC {
	var m integration.MAC
	m[0] = b
	return m
}

// keys returns the keys of a map[integration.MAC]bool as a slice, for use with
// require.ElementsMatch (order-independent comparison against a []integration.MAC
// result, e.g. from s.List()).
func keys(m map[integration.MAC]bool) []integration.MAC {
	ret := make([]integration.MAC, 0, len(m))
	for k := range m {
		ret = append(ret, k)
	}
	return ret
}

// list collects s.List() into a slice of MACs, stopping at the first error.
func list(s *Sftp, res integration.StorageResource) ([]integration.MAC, error) {
	var ret []integration.MAC
	for entry, err := range s.List(context.Background(), res, 0) {
		if err != nil {
			return nil, err
		}
		ret = append(ret, entry.MAC)
	}
	return ret, nil
}
