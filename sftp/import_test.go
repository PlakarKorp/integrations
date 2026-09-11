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
//	s := ts.newTestImportSftp(t, "/repo")
//	// ... seed files/dirs/symlinks directly on disk via ts.realPath ...
//	records, wait := runImporter(t, s)
//	got := drainRecords(records)
//	if err := wait(); err != nil { ... }
//	// ... assert against got ...

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/exclude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestImportSftp builds an *Sftp wired to the fake server's client, with
// excludes initialised (Import()'s walker dereferences s.excludes), ready to
// have its Import method exercised directly.
func (ts *testServer) newTestImportSftp(t *testing.T, rootDir string) *Sftp {
	s := ts.newTestSftp(t, rootDir)
	s.excludes = exclude.NewRuleSet()
	return s
}

// runImporter runs s.Import in a goroutine and returns the records channel
// for the caller to drain, plus a wait function that blocks until Import
// has returned and yields its final error.
//
// The required call order is always:
//  1. drain the returned channel to completion (drainRecords)
//  2. then call wait()
//
// Reversing the order deadlocks: workers block trying to send on a full
// records channel, Import blocks waiting for workers to finish, and the
// test blocks waiting for Import.
func runImporter(t *testing.T, s *Sftp) (<-chan *connectors.Record, func() error) {
	t.Helper()
	records := make(chan *connectors.Record, 16)
	done := make(chan error, 1)
	go func() {
		done <- s.Import(t.Context(), records, nil)
	}()

	return records, func() error { return <-done }
}

// drainRecords consumes every record off the channel until it is closed,
// returning them in receipt order for assertions.
func drainRecords(records <-chan *connectors.Record) []*connectors.Record {
	got := make([]*connectors.Record, 0, 16)
	for r := range records {
		got = append(got, r)
	}
	return got
}

// byPathname indexes a drained record slice by its SFTP-visible pathname,
// for convenient lookups/assertions regardless of the (non-deterministic,
// worker-pool-driven) order records arrive in.
func byPathname(records []*connectors.Record) map[string]*connectors.Record {
	m := make(map[string]*connectors.Record, len(records))
	for _, r := range records {
		m[r.Pathname] = r
	}
	return m
}

// keysOf returns the map's keys, for use in failure messages.
func keysOf(m map[string]*connectors.Record) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// seedTree builds one standard file/dir/symlink layout under root (a real,
// on-disk directory such as ts.realPath("/repo")), so import tests can
// share a single fixture instead of hand-rolling os.* calls per test.
//
// Resulting tree (relative to root):
//
//	root/                     <- e.g. ts.realPath("/repo"), mode 0750
//	├── file.txt               regular file, mode 0640, content "hello metadata"
//	├── subdir/                directory, mode 0750
//	│   └── nested.txt         regular file, mode 0600, content "nested"
//	├── link.txt                symlink -> file.txt
//	└── excluded/              directory, mode 0750 (target for tests that
//	    └── skip.txt           configure exclude rules; harmless otherwise)
//
// Record pathnames emitted by Import() are rooted under whatever rootDir
// the *Sftp connector was constructed with (see newTestImportSftp), not
// under "/". E.g. with rootDir "/repo", the file above surfaces as the
// record pathname "/repo/file.txt", not "/file.txt".
//
// It returns nothing; callers read expected values back via os.Lstat/
// os.Readlink against root/relative-path as needed, keeping a single source
// of truth (the real filesystem) rather than duplicated expectations.
func seedTree(t *testing.T, root string) {
	mustMkdir := func(p string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(p, mode); err != nil {
			t.Fatalf("seedTree: mkdir %s: %v", p, err)
		}
		// MkdirAll only applies mode to the leaf if it doesn't already
		// exist along the path (and is subject to umask), so force it.
		if err := os.Chmod(p, mode); err != nil {
			t.Fatalf("seedTree: chmod %s: %v", p, err)
		}
	}
	mustWrite := func(p string, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatalf("seedTree: write %s: %v", p, err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatalf("seedTree: chmod %s: %v", p, err)
		}
	}

	mustMkdir(root, 0750)
	mustWrite(filepath.Join(root, "file.txt"), "hello metadata", 0640)
	mustMkdir(filepath.Join(root, "subdir"), 0750)
	mustWrite(filepath.Join(root, "subdir", "nested.txt"), "nested", 0600)
	if err := os.Symlink("file.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("seedTree: symlink: %v", err)
	}
	mustMkdir(filepath.Join(root, "excluded"), 0750)
	mustWrite(filepath.Join(root, "excluded", "skip.txt"), "skip me", 0600)
}

func TestImport_WalksNestedTree(t *testing.T) {
	ts := newTestServer(t)
	seedTree(t, ts.realPath("/repo"))

	s := ts.newTestImportSftp(t, "/repo")
	defer s.Close(context.Background())

	records, wait := runImporter(t, s)
	got := drainRecords(records)
	require.NoError(t, wait())

	for _, r := range got {
		require.NoError(t, r.Err, "record %q has unexpected error", r.Pathname)
	}

	byPath := byPathname(got)

	// Every entry from seedTree's fixture must be emitted exactly once,
	// rooted under the connector's rootDir ("/repo").
	want := []string{
		"/repo",
		"/repo/file.txt",
		"/repo/subdir",
		"/repo/subdir/nested.txt",
		"/repo/link.txt",
		"/repo/excluded",
		"/repo/excluded/skip.txt",
	}
	for _, p := range want {
		assert.Contains(t, byPath, p, "missing record for %q; got paths: %v", p, keysOf(byPath))
	}
	assert.Len(t, got, len(want), "unexpected extra/missing records; got paths: %v", keysOf(byPath))
}

func TestImport_FileMetadata(t *testing.T) {
	ts := newTestServer(t)
	seedTree(t, ts.realPath("/repo"))

	// Ground truth: what the real filesystem reports for this file.
	want, err := os.Lstat(ts.realPath("/repo/file.txt"))
	if err != nil {
		t.Fatalf("os.Lstat: %v", err)
	}

	s := ts.newTestImportSftp(t, "/repo")
	defer s.Close(context.Background())

	records, wait := runImporter(t, s)
	got := drainRecords(records)
	require.NoError(t, wait())

	byPath := byPathname(got)
	for _, r := range got {
		require.NoError(t, r.Err, "record %q has unexpected error", r.Pathname)
	}

	rec, ok := byPath["/repo/file.txt"]
	require.True(t, ok, "expected a record for /repo/file.txt, got paths: %v", keysOf(byPath))

	fi := rec.FileInfo
	assert.Equal(t, want.Name(), fi.Lname)
	assert.Equal(t, want.Size(), fi.Lsize)
	assert.Equal(t, want.Mode(), fi.Lmode)
	assert.WithinDuration(t, want.ModTime(), fi.LmodTime, time.Second)

	// Uid/Gid are populated from the SFTP-protocol stat (*sftp.FileStat).
	// The seeded file is owned by the test process itself, so compare
	// against os.Getuid()/os.Getgid() rather than a hardcoded value.
	if uid := os.Getuid(); uid >= 0 {
		assert.Equal(t, uint64(uid), fi.Luid)
	}
	if gid := os.Getgid(); gid >= 0 {
		assert.Equal(t, uint64(gid), fi.Lgid)
	}
}

func TestImport_SymlinkTarget(t *testing.T) {
	ts := newTestServer(t)
	seedTree(t, ts.realPath("/repo"))

	// Ground truth: the symlink itself, not what it points to. Import()'s
	// walker uses Lstat (see walk()), so the emitted record describes the
	// link, not the resolved target.
	want, err := os.Lstat(ts.realPath("/repo/link.txt"))
	if err != nil {
		t.Fatalf("os.Lstat: %v", err)
	}
	wantTarget, err := os.Readlink(ts.realPath("/repo/link.txt"))
	if err != nil {
		t.Fatalf("os.Readlink: %v", err)
	}

	s := ts.newTestImportSftp(t, "/repo")
	defer s.Close(context.Background())

	records, wait := runImporter(t, s)
	got := drainRecords(records)
	require.NoError(t, wait())

	byPath := byPathname(got)
	for _, r := range got {
		require.NoError(t, r.Err, "record %q has unexpected error", r.Pathname)
	}

	rec, ok := byPath["/repo/link.txt"]
	require.True(t, ok, "expected a record for /repo/link.txt, got paths: %v", keysOf(byPath))

	fi := rec.FileInfo
	assert.Equal(t, want.Name(), fi.Lname)
	assert.Equal(t, want.Mode(), fi.Lmode)
	assert.WithinDuration(t, want.ModTime(), fi.LmodTime, time.Second)

	// Target ("originFile" in walkDir_worker) is populated via ReadLink
	// for symlinks, and should match the raw (unresolved) link target.
	assert.Equal(t, wantTarget, rec.Target)

	// A non-symlink record (regular file) must have an empty Target.
	fileRec, ok := byPath["/repo/file.txt"]
	require.True(t, ok, "expected a record for /repo/file.txt, got paths: %v", keysOf(byPath))
	assert.Empty(t, fileRec.Target)
}

// TestImport_ExcludeRules covers all exclusion scenarios in one table-driven
// test. Each case seeds the standard tree, applies different exclude rules,
// and asserts which paths are absent and which remain. Adding a new scenario
// requires only a new entry in the table, not a new top-level function.
func TestImport_ExcludeRules(t *testing.T) {
	tests := []struct {
		name    string
		rules   []string
		absent  []string
		present []string
	}{
		{
			// Excluding a glob that covers a directory and its descendants
			// must remove both the directory record and every record below it.
			name:  "skip_dir_and_descendants",
			rules: []string{"/repo/excluded/**"},
			absent: []string{
				"/repo/excluded",
				"/repo/excluded/skip.txt",
			},
			present: []string{
				"/repo",
				"/repo/file.txt",
				"/repo/subdir",
				"/repo/subdir/nested.txt",
				"/repo/link.txt",
			},
		},
		{
			// Excluding a leaf file must remove only that file; the parent
			// directory record must still appear, proving exclusion is not an
			// overbroad parent-prefix filter.
			name:  "skip_leaf_file_only",
			rules: []string{"/repo/excluded/skip.txt"},
			absent: []string{
				"/repo/excluded/skip.txt",
			},
			present: []string{
				"/repo",
				"/repo/file.txt",
				"/repo/subdir",
				"/repo/subdir/nested.txt",
				"/repo/link.txt",
				"/repo/excluded", // parent directory must still appear
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t)
			seedTree(t, ts.realPath("/repo"))

			s := ts.newTestImportSftp(t, "/repo")
			defer s.Close(context.Background())

			require.NoError(t, s.excludes.AddRulesFromArray(tc.rules),
				"failed to add exclude rules")

			records, wait := runImporter(t, s)
			got := drainRecords(records)
			require.NoError(t, wait())

			byPath := byPathname(got)
			for _, r := range got {
				require.NoError(t, r.Err, "record %q has unexpected error", r.Pathname)
			}

			t.Run("absent", func(t *testing.T) {
				for _, p := range tc.absent {
					assert.NotContains(t, byPath, p,
						"excluded path %q should be absent; got: %v", p, keysOf(byPath))
				}
			})

			t.Run("present", func(t *testing.T) {
				for _, p := range tc.present {
					assert.Contains(t, byPath, p,
						"expected path %q to be present; got: %v", p, keysOf(byPath))
				}
				assert.Len(t, got, len(tc.present),
					"unexpected record count; got paths: %v", keysOf(byPath))
			})
		})
	}
}

func TestWalk_RootLstatError(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestImportSftp(t, "/repo")
	defer s.Close(context.Background())

	records, wait := runImporter(t, s)
	got := drainRecords(records)
	err := wait()

	// walkDir_walker's callback swallows a per-path error (including the
	// root's own Lstat failure) into an error record and returns nil, so
	// Import() itself completes without error; the failure is only ever
	// visible via the emitted record below.
	require.NoError(t, err, "Import should not itself return an error for a root Lstat failure")

	// The root Lstat error should be emitted as a record.
	require.Len(t, got, 1, "expected exactly one record for the root Lstat error")
	rec := got[0]
	assert.Equal(t, "/repo", rec.Pathname)
	assert.Error(t, rec.Err, "expected record for root Lstat error to have non-nil Err")
	assert.Contains(t, rec.Err.Error(), "file does not exist", "unexpected record error message: %v", rec.Err)
}

func TestImport_ContextCancellation(t *testing.T) {
	ts := newTestServer(t)
	seedTree(t, ts.realPath("/repo"))

	s := ts.newTestImportSftp(t, "/repo")
	defer s.Close(context.Background())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// runImporter always uses t.Context(), so we wire channels manually here
	// to pass the pre-cancelled context while preserving the mandatory
	// drain-before-wait order.
	records := make(chan *connectors.Record, 16)
	done := make(chan error, 1)
	go func() {
		done <- s.Import(ctx, records, nil)
	}()

	got := drainRecords(records)
	err := <-done

	require.Error(t, err, "expected Import to return an error when context is already cancelled")
	assert.ErrorIs(t, err, context.Canceled, "unexpected error: %v", err)

	// No records should have been produced: the walk aborts on the
	// root entry before any job is ever dispatched to the workers.
	assert.Empty(t, got, "expected no records once ctx is cancelled up front, got: %v", keysOf(byPathname(got)))
}

// TestImport_WorkerPoolConcurrency exercises walkDir_walker with more than
// one worker (maxConcurrency > 1) fanning out over a tree wide enough that,
// on any reasonable scheduler, multiple workers actually run concurrently.
// It asserts every seeded entry is emitted exactly once - no duplicates, no
// drops, no interleaving corruption across workers/records - and should be
// run with `go test -race` to catch any data race in walkDir_worker/records.
func TestImport_WorkerPoolConcurrency(t *testing.T) {
	ts := newTestServer(t)

	root := ts.realPath("/repo")
	if err := os.MkdirAll(root, 0750); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	// Seed a flat set of files wide enough to keep several workers busy
	// concurrently; each file's content is unique so we can also detect
	// any cross-talk between workers if records were ever corrupted.
	const numFiles = 64
	want := make([]string, 0, numFiles+1)
	want = append(want, "/repo")
	for i := range numFiles {
		name := fmt.Sprintf("file-%03d.txt", i)
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, fmt.Appendf(nil, "content-%03d", i), 0640); err != nil {
			t.Fatalf("seed file %s: %v", name, err)
		}
		want = append(want, "/repo/"+name)
	}

	s := ts.newTestImportSftp(t, "/repo")
	s.maxConcurrency = 8
	defer s.Close(context.Background())

	records, wait := runImporter(t, s)
	got := drainRecords(records)
	require.NoError(t, wait())

	for _, r := range got {
		require.NoError(t, r.Err, "record %q has unexpected error", r.Pathname)
	}

	byPath := byPathname(got)
	for _, p := range want {
		assert.Contains(t, byPath, p, "missing record for %q; got paths: %v", p, keysOf(byPath))
	}
	assert.Len(t, got, len(want), "unexpected extra/missing records (duplicate or dropped under concurrency); got paths: %v", keysOf(byPath))

	// No pathname should ever appear more than once - a worker-pool bug
	// (e.g. jobs shared/re-processed across workers) would surface here
	// as duplicate records rather than a mismatched count above.
	seen := make(map[string]int, len(got))
	for _, r := range got {
		seen[r.Pathname]++
	}
	for p, n := range seen {
		assert.Equal(t, 1, n, "pathname %q was emitted %d times, expected exactly once", p, n)
	}
}

// TestImport_FileReaderReturnsContent verifies that the lazy reader attached
// to each regular-file record actually opens and streams the remote file's
// content when called. This exercises the client.Open callback installed by
// walkDir_worker (see import.go) which metadata-only tests never invoke —
// a regression in path capture or client state would only surface here.
func TestImport_FileReaderReturnsContent(t *testing.T) {
	ts := newTestServer(t)

	root := ts.realPath("/repo")
	if err := os.MkdirAll(root, 0750); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	wantContent := []byte("hello from reader test")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), wantContent, 0640); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	s := ts.newTestImportSftp(t, "/repo")
	defer s.Close(context.Background())

	records, wait := runImporter(t, s)
	got := drainRecords(records)
	require.NoError(t, wait())

	byPath := byPathname(got)
	rec, ok := byPath["/repo/file.txt"]
	require.True(t, ok, "expected a record for /repo/file.txt; got: %v", keysOf(byPath))
	require.NoError(t, rec.Err, "unexpected error on /repo/file.txt record")

	// Reader is a LazyReader: the underlying client.Open runs on first Read,
	// not when the record was emitted. This call exercises that deferred open.
	require.NotNil(t, rec.Reader, "expected a non-nil Reader on /repo/file.txt record")
	defer rec.Reader.Close()

	data, err := io.ReadAll(rec.Reader)
	require.NoError(t, err, "io.ReadAll from lazy Reader failed")
	assert.Equal(t, wantContent, data, "file content did not round-trip correctly through the lazy reader")
}
