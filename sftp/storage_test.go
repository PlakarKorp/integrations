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
	"testing"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
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
			if got != tt.want {
				t.Fatalf("s.path(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestStorageCreate_FreshRepo(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	// Check that the repo directory was created with the correct permissions.
	info, err := ts.realStat("/repo")
	if err != nil {
		t.Fatalf("failed to stat repo directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected /repo to be a directory, got: %v", info.Mode())
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("expected /repo to have permissions 0700, got: %v", info.Mode().Perm())
	}
	// Check that the packfiles and states directories were created.
	if _, err := ts.realStat("/repo/packfiles"); err != nil {
		t.Fatalf("failed to stat packfiles directory: %v", err)
	}
	if _, err := ts.realStat("/repo/states"); err != nil {
		t.Fatalf("failed to stat states directory: %v", err)
	}
	// Check that the locks directory was created.
	if _, err := ts.realStat("/repo/locks"); err != nil {
		t.Fatalf("failed to stat locks directory: %v", err)
	}
}

func TestStorageCreate_NonEmptyRepo(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	// Create a non-empty directory at /repo.
	if err := os.MkdirAll(ts.realPath("/repo"), 0700); err != nil {
		t.Fatalf("failed to create non-empty repo directory: %v", err)
	}
	if err := os.WriteFile(ts.realPath("/repo/somefile"), []byte("data"), 0600); err != nil {
		t.Fatalf("failed to create file in non-empty repo directory: %v", err)
	}

	err := s.Create(context.Background(), []byte("test config"))
	if err == nil {
		t.Fatalf("s.Create() initialised a non empty directory")
	}
}

func TestStorageOpen(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close((context.Background()))

	s.Create(context.Background(), []byte("test config"))
	_, err := s.Open(context.Background())

	if err != nil {
		t.Fatalf("failed to open CONFIG file")
	}
}

func TestStorageOpen_MissingConfig(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close((context.Background()))

	_, err := s.Open(context.Background())

	if err == nil {
		t.Fatalf("expected error when opening missing CONFIG file")
	}
}

func TestStorageMetadata(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close((context.Background()))

	_, err := s.Mode(context.Background())
	if err != nil {
		t.Fatalf("failed to access s.Mode()")
	}

	size, err := s.Size(context.Background())
	if err != nil {
		t.Fatalf("failed to access s.Mode()")
	}
	if size != -1 {
		t.Fatalf("s.Size() should return -1")
	}
}

func TestStorageList_Supported(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close((context.Background()))

	s.Create(context.Background(), []byte("test config"))
	supportedStorageFiletypes := []storage.StorageResource{
		storage.StorageResourcePackfile,
		storage.StorageResourceLock,
		storage.StorageResourceState,
	}
	for _, tt := range supportedStorageFiletypes {
		t.Run(tt.String(), func(t *testing.T) {
			_, err := s.List(context.Background(), tt)
			if err != nil {
				t.Fatalf("failed to access s.List() for %s: %s", tt.String(), err.Error())
			}

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
			if err == nil {
				t.Fatalf("expected error when accessing s.List() for %s", tt.String())
			}

		})
	}
}

func TestStorageGetPut_Packfile(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	want := []byte("test packfile data")
	m := mac(0x2a)

	if _, err := s.Put(context.Background(), storage.StorageResourcePackfile, m, bytes.NewReader(want)); err != nil {
		t.Fatalf("s.Put() failed: %v", err)
	}

	rc, err := s.Get(context.Background(), storage.StorageResourcePackfile, m, nil)
	if err != nil {
		t.Fatalf("s.Get() failed: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("failed to read packfile: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("packfile contents = %q, want %q", got, want)
	}
}

func TestStorageGetPut_State(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	want := []byte("test packfile data")
	m := mac(0x2a)

	if _, err := s.Put(context.Background(), storage.StorageResourceState, m, bytes.NewReader(want)); err != nil {
		t.Fatalf("s.Put() failed: %v", err)
	}

	rc, err := s.Get(context.Background(), storage.StorageResourceState, m, nil)
	if err != nil {
		t.Fatalf("s.Get() failed: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("failed to read packfile: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("packfile contents = %q, want %q", got, want)
	}
}

func TestStorageGet_PackfileRange(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	want := []byte("test packfile data")
	m := mac(0x2a)

	if _, err := s.Put(context.Background(), storage.StorageResourcePackfile, m, bytes.NewReader(want)); err != nil {
		t.Fatalf("s.Put() failed: %v", err)
	}

	rc, err := s.Get(context.Background(), storage.StorageResourcePackfile, m, &storage.Range{Offset: 5, Length: 8})
	if err != nil {
		t.Fatalf("s.Get() failed: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("failed to read packfile: %v", err)
	}
	if string(got) != string(want)[5:5+8] {
		t.Fatalf("packfile contents = %q, want %q", got, want[5:5+8])
	}

}

func TestStorageGet_StateRangeUnsupported(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	want := []byte("test packfile data")
	m := mac(0x2a)

	if _, err := s.Put(context.Background(), storage.StorageResourceState, m, bytes.NewReader(want)); err != nil {
		t.Fatalf("s.Put() failed: %v", err)
	}

	_, err := s.Get(context.Background(), storage.StorageResourceState, m, &storage.Range{Offset: 5, Length: 8})
	if err == nil {
		t.Fatalf("s.Get() expected to fail for state range request")
	}
}

func TestStorageGetPut_Lock(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	want := []byte("test packfile data")
	m := mac(0x2a)

	if _, err := s.Put(context.Background(), storage.StorageResourceLock, m, bytes.NewReader(want)); err != nil {
		t.Fatalf("s.Put() failed: %v", err)
	}

	rc, err := s.Get(context.Background(), storage.StorageResourceLock, m, nil)
	if err != nil {
		t.Fatalf("s.Get() expected to fail for state range request")
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("failed to read packfile: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("packfile contents = %q, want %q", got, want)
	}

}

func TestStorageGet_LockRangeUnsupported(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	want := []byte("test packfile data")
	m := mac(0x2a)

	if _, err := s.Put(context.Background(), storage.StorageResourceLock, m, bytes.NewReader(want)); err != nil {
		t.Fatalf("s.Put() failed: %v", err)
	}

	_, err := s.Get(context.Background(), storage.StorageResourceLock, m, &storage.Range{Offset: 5, Length: 8})
	if err == nil {
		t.Fatalf("s.Get() expected to fail for state range request")
	}
}

func TestStorageDelete_Packfile(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	m := mac(0x2a)
	want := []byte("test packfile data")
	s.Put(context.Background(), storage.StorageResourcePackfile, m, bytes.NewReader(want))

	if err := s.Delete(context.Background(), storage.StorageResourcePackfile, m); err != nil {
		t.Fatalf("s.Put() failed: %v", err)
	}
}

func TestStorageDelete_State(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	m := mac(0x2a)
	want := []byte("test packfile data")
	s.Put(context.Background(), storage.StorageResourceState, m, bytes.NewReader(want))

	if err := s.Delete(context.Background(), storage.StorageResourceState, m); err != nil {
		t.Fatalf("s.Put() failed: %v", err)
	}
}

func TestStorageDelete_LockNonExistent(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	m := mac(0x2a)

	if err := s.Delete(context.Background(), storage.StorageResourceLock, m); err == nil {
		t.Fatalf("s.Put() expected a failure")
	}
}

func TestStorageDelete_UnsupportedResource(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	m := mac(0x2a)

	if err := s.Delete(context.Background(), storage.StorageResourceUndefined, m); err == nil {
		t.Fatalf("s.Put() expected a failure")
	}
}

func TestStorageGetLocks_IgnoresBadNames(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestSftp(t, "/repo")
	defer s.Close(context.Background())

	if err := s.Create(context.Background(), []byte("test config")); err != nil {
		t.Fatalf("s.Create() failed: %v", err)
	}

	// Two valid locks, put through the real API.
	want := map[objects.MAC]bool{
		mac(0x11): true,
		mac(0x22): true,
	}
	for m := range want {
		if _, err := s.Put(context.Background(), storage.StorageResourceLock, m, bytes.NewReader([]byte("data"))); err != nil {
			t.Fatalf("s.Put() failed: %v", err)
		}
	}

	// Bad entries written directly to disk, bypassing the Sftp API.
	if err := os.WriteFile(ts.realPath("/repo/locks/not-a-hexname"), []byte("x"), 0600); err != nil {
		t.Fatalf("failed to write bad lock file: %v", err)
	}
	if err := os.WriteFile(ts.realPath("/repo/locks/deadbeef"), []byte("x"), 0600); err != nil {
		t.Fatalf("failed to write bad lock file: %v", err)
	}

	got, err := s.List(context.Background(), storage.StorageResourceLock)
	if err != nil {
		t.Fatalf("s.List() failed: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("s.List() returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for _, m := range got {
		if !want[m] {
			t.Fatalf("s.List() returned unexpected mac %x", m)
		}
	}
}

func TestStorageConcurrentPacksAndStates(t *testing.T) {
	t.Skip("TODO")
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
