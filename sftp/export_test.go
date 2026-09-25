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
//	s := ts.newTestExportSftp(t, "/repo")
//	records := make(chan *connectors.Record, 16)
//	results, wait := runExporter(t, s, records)
//	records <- connectors.NewRecord(...)
//	close(records)
//	got := drainResults(results)
//	// ... assert against got, and/or against ts.realPath(...) on disk ...

import (
	"bytes"
	"io"
	"os"
	"runtime"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/exclude"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestExportSftp builds an *Sftp wired to the fake server's client, with
// excludes initialised (mirroring newTestImportSftp), ready to have its
// Export method exercised directly. setOwner defaults to false; tests that
// need chown behaviour should set s.setOwner = true after construction.
func (ts *testServer) newTestExportSftp(t *testing.T, rootDir string) *Sftp {
	s := ts.newTestSftp(t, rootDir)
	s.excludes = exclude.NewRuleSet()
	return s
}

// runExporter runs s.Export in a goroutine, feeding it the given records
// channel (the caller is responsible for sending records and closing it),
// and returns the results channel for the caller to drain, plus a wait
// function that blocks until Export has returned and yields its final
// error.
//
// The required call order is always:
//  1. send all records into the input channel, then close it
//  2. drain the results channel to completion (drainResults)
//  3. then call wait()
//
// Draining before wait matters because Export's file/symlink tasks send
// results concurrently. If results fills up, those tasks block, Export
// cannot finish, and wait() deadlocks.
func runExporter(t *testing.T, s *Sftp, records <-chan *connectors.Record) (<-chan *connectors.Result, func() error) {
	results := make(chan *connectors.Result, 16)
	done := make(chan error, 1)
	go func() {
		done <- s.Export(t.Context(), records, results)
	}()

	return results, func() error { return <-done }
}

// drainResults consumes every result off the channel until it is closed,
// returning them in receipt order for assertions.
func drainResults(results <-chan *connectors.Result) []*connectors.Result {
	got := make([]*connectors.Result, 0, 16)
	for r := range results {
		got = append(got, r)
	}
	return got
}

func TestExport_DirectoryCreate(t *testing.T) {
	ts := newTestServer(t)
	// Export's directory() calls Mkdir for a single path component; the
	// parent directory must already exist on disk (mirroring a real SFTP
	// server, which doesn't auto-create intermediate directories either).
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	records <- connectors.NewRecord("/dir", "", objects.FileInfo{Lmode: os.ModeDir | 0750}, nil, nil)
	close(records)

	got := drainResults(results)
	require.NoError(t, wait(), "Export should not return an error for a simple directory create")

	assert.Len(t, got, 1, "expected exactly one result for the directory create")
	assert.Equal(t, "/dir", got[0].Record.Pathname, "unexpected pathname in the result for the directory create")
	assert.NoError(t, got[0].Err, "expected no error in the result for the directory create")

	// Verify that the directory was actually created on the fake SFTP
	// server. Use the client (SFTP-visible path "/repo/dir"), not
	// ts.realPath, since the client operates in the fake server's own
	// path namespace rather than the real on-disk path.
	dirInfo, err := ts.client.Stat("/repo/dir")
	require.NoError(t, err, "expected Stat to succeed for the created directory")
	assert.True(t, dirInfo.IsDir(), "expected the created path to be a directory")
	assert.Equal(t, os.FileMode(0750)|os.ModeDir, dirInfo.Mode(), "unexpected mode for the created directory")
}

func TestExport_RootDirectoryIdempotent(t *testing.T) {
	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Exporting the root directory itself should be a no-op (idempotent).
	records <- connectors.NewRecord("/", "", objects.FileInfo{Lmode: os.ModeDir | 0750}, nil, nil)
	close(records)
	got := drainResults(results)

	require.NoError(t, wait(), "Export should not return an error for exporting the root directory")
	assert.Len(t, got, 1, "expected exactly one result for the root directory export")
	assert.Equal(t, "/", got[0].Record.Pathname, "unexpected pathname in the result for the root directory export")
	assert.NoError(t, got[0].Err, "expected no error in the result for the root directory export")

	// Verify that the root directory still exists and has the expected mode.
	rootInfo, err := ts.client.Stat("/repo")
	require.NoError(t, err, "expected Stat to succeed for the root directory")
	assert.True(t, rootInfo.IsDir(), "expected the root path to be a directory")
	assert.Equal(t, os.FileMode(0750)|os.ModeDir, rootInfo.Mode(), "unexpected mode for the root directory")

}

func TestExport_FileWrite(t *testing.T) {
	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	content := []byte("Hello, SFTP!")
	records <- connectors.NewRecord("/file.txt", "", objects.FileInfo{Lmode: 0644}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	})
	close(records)

	got := drainResults(results)
	require.NoError(t, wait(), "Export should not return an error for a simple file write")

	assert.Len(t, got, 1, "expected exactly one result for the file write")
	assert.Equal(t, "/file.txt", got[0].Record.Pathname, "unexpected pathname in the result for the file write")
	assert.NoError(t, got[0].Err, "expected no error in the result for the file write")

	// Verify that the file was actually created on the fake SFTP server.
	fileInfo, err := ts.client.Stat("/repo/file.txt")
	require.NoError(t, err, "expected Stat to succeed for the created file")
	assert.False(t, fileInfo.IsDir(), "expected the created path to be a file")
	assert.Equal(t, os.FileMode(0644), fileInfo.Mode(), "unexpected mode for the created file")

	// Verify the content of the file.
	f, err := ts.client.Open("/repo/file.txt")
	require.NoError(t, err, "expected Open to succeed for the created file")
	defer f.Close()
	fileContent, err := io.ReadAll(f)
	require.NoError(t, err, "expected reading the created file to succeed")
	assert.Equal(t, content, fileContent, "unexpected content in the created file")
}

func TestExport_SymlinkCreate(t *testing.T) {
	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Create a symlink from /link.txt to /file.txt
	records <- connectors.NewRecord("/link.txt", "/file.txt", objects.FileInfo{Lmode: os.ModeSymlink | 0777}, nil, nil)
	close(records)

	got := drainResults(results)
	require.NoError(t, wait(), "Export should not return an error for a simple symlink create")

	assert.Len(t, got, 1, "expected exactly one result for the symlink create")
	assert.Equal(t, "/link.txt", got[0].Record.Pathname, "unexpected pathname in the result for the symlink create")
	assert.NoError(t, got[0].Err, "expected no error in the result for the symlink create")

	// Verify that the symlink was actually created on the fake SFTP server.
	// Note: symlink() (export.go) intentionally never chmods the link -
	// sftp has no lchown(2)/lchmod(2) equivalent - so its mode is whatever
	// the underlying os.Symlink call produces, not the record's Lmode.
	// Only the symlink bit itself is meaningful to assert here.
	linkInfo, err := ts.client.Lstat("/repo/link.txt")
	require.NoError(t, err, "expected Lstat to succeed for the created symlink")
	assert.True(t, linkInfo.Mode()&os.ModeSymlink != 0, "expected the created path to be a symlink, got mode %v", linkInfo.Mode())

	// Verify that the symlink points to the correct target.
	target, err := ts.client.ReadLink("/repo/link.txt")
	require.NoError(t, err, "expected ReadLink to succeed for the created symlink")
	assert.Equal(t, "/file.txt", target, "unexpected target for the created symlink")
}

func TestExport_SymlinkFailsIfExists(t *testing.T) {
	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	// Pre-create a file at the target path so Symlink() will fail.
	if err := os.WriteFile(ts.realPath("/repo/link.txt"), []byte("existing"), 0644); err != nil {
		t.Fatalf("pre-create file: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	records <- connectors.NewRecord("/link.txt", "/file.txt", objects.FileInfo{Lmode: os.ModeSymlink | 0777}, nil, nil)
	close(records)

	got := drainResults(results)

	// Export itself must not return a terminal error — symlink failures are
	// per-record, not fatal to the whole operation.
	require.NoError(t, wait())
	require.Len(t, got, 1)
	assert.Error(t, got[0].Err, "expected a per-record error when symlink target already exists")
}

func TestExport_ChownAppliedWhenSetOwner(t *testing.T) {
	if runtime.GOOS == "windows" { 
		// os.Chown is unconditionally unsupported on Windows (always
		// returns syscall.EWINDOWS), so there is no real uid/gid
		// ownership for this test to verify.
		t.Skip()
	}

	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	s.setOwner = true
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Use the current process uid/gid so Chown succeeds without root.
	uid := uint64(os.Getuid())
	gid := uint64(os.Getgid())
	content := []byte("chown test")
	records <- connectors.NewRecord("/file.txt", "",
		objects.FileInfo{Lmode: 0644, Luid: uid, Lgid: gid},
		nil,
		func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(content)), nil
		},
	)
	close(records)

	got := drainResults(results)
	require.NoError(t, wait())
	require.Len(t, got, 1)
	assert.NoError(t, got[0].Err, "chown with current uid/gid should succeed")

	// Verify the file was written and ownership matches.
	info, err := ts.realStat("/repo/file.txt")
	require.NoError(t, err)
	gotUid, gotGid := fileOwner(t, info)
	assert.Equal(t, uid, gotUid, "file uid must match the record's Luid")
	assert.Equal(t, gid, gotGid, "file gid must match the record's Lgid")
}

func TestExport_HardlinkCanonicalOnce(t *testing.T) {
	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Create a hardlink from /hardlink.txt to /file.txt. Lnlink must be >1
	// for file() to route through hardlink() (see export.go); a nil read
	// func would otherwise panic once anything actually reads the lazy
	// Reader, so provide real content too.
	content := []byte("hardlinked content")
	records <- connectors.NewRecord("/hardlink.txt", "/file.txt", objects.FileInfo{Lmode: 0644, Lnlink: 2}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	})
	close(records)

	got := drainResults(results)
	require.NoError(t, wait(), "Export should not return an error for a simple hardlink create")

	assert.Len(t, got, 1, "expected exactly one result for the hardlink create")
	assert.Equal(t, "/hardlink.txt", got[0].Record.Pathname, "unexpected pathname in the result for the hardlink create")
	assert.NoError(t, got[0].Err, "expected no error in the result for the hardlink create")

	// Verify that the hardlink was actually created on the fake SFTP server.
	hardlinkInfo, err := ts.client.Stat("/repo/hardlink.txt")
	require.NoError(t, err, "expected Stat to succeed for the created hardlink")
	assert.False(t, hardlinkInfo.IsDir(), "expected the created path to be a file")
	assert.Equal(t, os.FileMode(0644), hardlinkInfo.Mode(), "unexpected mode for the created hardlink")
}

func TestExport_HardlinkConcurrentRace(t *testing.T) {
	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Create two hardlinks to the same target concurrently. Both should
	// succeed, and the underlying file should only be created once.
	content := []byte("concurrent hardlink content")
	records <- connectors.NewRecord("/hardlink1.txt", "/file.txt", objects.FileInfo{Lmode: 0644, Lnlink: 2}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	})
	records <- connectors.NewRecord("/hardlink2.txt", "/file.txt", objects.FileInfo{Lmode: 0644, Lnlink: 2}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	})
	close(records)

	got := drainResults(results)
	require.NoError(t, wait(), "Export should not return an error for concurrent hardlink creates")

	assert.Len(t, got, 2, "expected exactly two results for the concurrent hardlink creates")
	for _, r := range got {
		assert.NoError(t, r.Err, "expected no error in the result for the concurrent hardlink create: %v", r.Record.Pathname)
	}

	// Verify that both paths exist with the expected mode.
	for _, p := range []string{"/repo/hardlink1.txt", "/repo/hardlink2.txt"} {
		info, err := ts.client.Stat(p)
		require.NoError(t, err, "expected Stat to succeed for the created hardlink: %v", p)
		assert.False(t, info.IsDir(), "expected the created path to be a file: %v", p)
		assert.Equal(t, os.FileMode(0644), info.Mode(), "unexpected mode for the created hardlink: %v", p)
	}

	// The defining property of a hard link is a shared inode: both paths
	// must point to the same underlying file, not two independent copies.
	// os.SameFile compares the inode and device from real os.Lstat, which
	// is why we use ts.realPath rather than the SFTP client here.
	info1, err := ts.realStat("/repo/hardlink1.txt")
	require.NoError(t, err, "realStat hardlink1")
	info2, err := ts.realStat("/repo/hardlink2.txt")
	require.NoError(t, err, "realStat hardlink2")
	assert.True(t, os.SameFile(info1, info2),
		"hardlink1.txt and hardlink2.txt must share the same inode")
}

func TestExport_PermissionsAppliedBottomUp(t *testing.T) {
	ts := newTestServer(t)
	// Forcefully restore permissions on cleanup so t.TempDir can remove the
	// directory tree. Without this, os.RemoveAll fails with permission denied
	// because Export leaves the parent directory as 0500 (read/execute only).
	t.Cleanup(func() {
		_ = os.Chmod(ts.realPath("/repo/parent"), 0750)
		_ = os.Chmod(ts.realPath("/repo/parent/child"), 0750)
	})

	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Send parent before child, with a restrictive parent mode (0500:
	// read+execute only, no write). If Export applied the parent's chmod
	// immediately on receipt, the subsequent Mkdir for the child would
	// fail because 0500 forbids creating entries inside the directory.
	// Export must defer all directory chmod calls until after every record
	// has been processed and apply them in reverse order (child before
	// parent), which is what the dirPerms reverse loop in export.go does.
	records <- connectors.NewRecord("/parent", "", objects.FileInfo{Lmode: os.ModeDir | 0500}, nil, nil)
	records <- connectors.NewRecord("/parent/child", "", objects.FileInfo{Lmode: os.ModeDir | 0750}, nil, nil)
	close(records)

	got := drainResults(results)
	require.NoError(t, wait(), "Export should not return an error when parent has restrictive final mode")

	require.Len(t, got, 2)
	for _, r := range got {
		assert.NoError(t, r.Err, "unexpected error for %q", r.Record.Pathname)
	}

	// Both directories must exist with their requested modes.
	parentInfo, err := ts.client.Stat("/repo/parent")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0500)|os.ModeDir, parentInfo.Mode())

	childInfo, err := ts.client.Stat("/repo/parent/child")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0750)|os.ModeDir, childInfo.Mode())
}

func TestExport_ChownNoopByDefault(t *testing.T) {
	ts := newTestServer(t)
	// Pre-create the root directory with a different owner than the record
	// will specify, to verify that Export does not attempt to chown by
	// default.
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Export a directory with a different owner than the pre-created one.
	records <- connectors.NewRecord("/dir", "", objects.FileInfo{Lmode: os.ModeDir | 0750, Luid: 1001, Lgid: 1001}, nil, nil)
	close(records)

	got := drainResults(results)
	require.NoError(t, wait(), "Export should not return an error for a simple directory create with chown info")

	assert.Len(t, got, 1, "expected exactly one result for the directory create with chown info")
	assert.Equal(t, "/dir", got[0].Record.Pathname, "unexpected pathname in the result for the directory create with chown info")
	assert.NoError(t, got[0].Err, "expected no error in the result for the directory create with chown info")

	// Verify that the directory was actually created on the fake SFTP server
	// and that its owner has not changed (i.e., it remains the same as the
	// pre-created directory).
	dirInfo, err := ts.client.Stat("/repo/dir")
	require.NoError(t, err, "expected Stat to succeed for the created directory")
	assert.True(t, dirInfo.IsDir(), "expected the created path to be a directory")
	assert.Equal(t, os.FileMode(0750)|os.ModeDir, dirInfo.Mode(), "unexpected mode for the created directory")

}

func TestExport_ErrorIsolation(t *testing.T) {
	ts := newTestServer(t)
	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Send a record that will cause an error (e.g., trying to create a file
	// in a non-existent directory).
	records <- connectors.NewRecord("/nonexistentdir/file.txt", "", objects.FileInfo{Lmode: 0644}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("content"))), nil
	})
	close(records)

	got := drainResults(results)
	require.NoError(t, wait(), "Export should not return an error for a record that causes an isolated error")

	assert.Len(t, got, 1, "expected exactly one result for the record that causes an isolated error")
	assert.Equal(t, "/nonexistentdir/file.txt", got[0].Record.Pathname, "unexpected pathname in the result for the record that causes an isolated error")
	assert.Error(t, got[0].Err, "expected an error in the result for the record that causes an isolated error")
	// The error should indicate that the parent directory does not exist.
	// Note: this goes through the SFTP protocol (writeAtomic's temp-file
	// creation), so the message is pkg/sftp's own wording for
	// SSH_FX_NO_SUCH_FILE, not the raw OS errno string.
	assert.Contains(t, got[0].Err.Error(), "file does not exist", "unexpected error message for the record that causes an isolated error: %v", got[0].Err)
}

func TestExport_XattrRecordsSkipped(t *testing.T) {
	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Records marked IsXattr should be acked immediately without any
	// filesystem action (see Export's `if record.IsXattr` branch).
	rec := connectors.NewXattr("/file.txt", "user.test", objects.AttributeExtended, nil)
	require.True(t, rec.IsXattr, "test setup: NewXattr should produce a record with IsXattr set")
	records <- rec
	close(records)

	got := drainResults(results)
	require.NoError(t, wait(), "Export should not return an error for xattr records")

	require.Len(t, got, 1, "expected exactly one result for the xattr record")
	assert.Equal(t, "/file.txt", got[0].Record.Pathname, "unexpected pathname in the result for the xattr record")
	assert.NoError(t, got[0].Err, "xattr records should be acked without error")

	// No file should have been created on disk: the xattr record was
	// never written to, only acked.
	_, err := ts.client.Stat("/repo/file.txt")
	assert.Error(t, err, "expected no file to have been created for a skipped xattr record")
}

func TestExport_PathTraversalEscape(t *testing.T) {
	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	newFileRecord := func(pathname, content string) *connectors.Record {
		return connectors.NewRecord(pathname, "", objects.FileInfo{Lmode: 0644}, nil, func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(content))), nil
		})
	}

	// Two well-behaved records that stay within the restore root, and one
	// hostile record trying to climb out of /repo and write into a
	// sibling directory outside the restore root.
	records <- newFileRecord("/good1.txt", "good1-content")
	records <- newFileRecord("/../../outside.txt", "escaped-content")
	records <- newFileRecord("/good2.txt", "good2-content")
	close(records)

	got := drainResults(results)
	_ = wait()

	require.Len(t, got, 3, "expected exactly three results, one per record")

	byPath := make(map[string]*connectors.Result, len(got))
	for _, r := range got {
		byPath[r.Record.Pathname] = r
	}

	// The two legitimate files should succeed and land inside /repo.
	require.Contains(t, byPath, "/good1.txt")
	assert.NoError(t, byPath["/good1.txt"].Err, "expected the first well-behaved file to succeed")
	good1, err := ts.client.Open("/repo/good1.txt")
	require.NoError(t, err, "expected the first well-behaved file to exist inside the restore root")
	good1Content, err := io.ReadAll(good1)
	require.NoError(t, err)
	good1.Close()
	assert.Equal(t, "good1-content", string(good1Content))

	require.Contains(t, byPath, "/good2.txt")
	assert.NoError(t, byPath["/good2.txt"].Err, "expected the second well-behaved file to succeed")
	good2, err := ts.client.Open("/repo/good2.txt")
	require.NoError(t, err, "expected the second well-behaved file to exist inside the restore root")
	good2Content, err := io.ReadAll(good2)
	require.NoError(t, err)
	good2.Close()
	assert.Equal(t, "good2-content", string(good2Content))

	// The hostile record should fail with a path containment error, and
	// nothing should be written outside of /repo on the fake server's
	// filesystem.
	require.Contains(t, byPath, "/../../outside.txt")
	assert.Error(t, byPath["/../../outside.txt"].Err, "expected Export to reject a path that escapes the restore root")

	_, statErr := ts.client.Stat("/outside.txt")
	assert.Error(t, statErr, "expected no file to have been written outside of the restore root")
}

func TestIsContained(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		joined string
		want   bool
	}{
		// --- root "/" (filesystem root): every absolute path is contained ---
		{"root-slash exact match", "/", "/", true},
		{"root-slash top-level child", "/", "/toto", true},
		{"root-slash nested child", "/", "/toto/titi.txt", true},
		{"root-slash double-slash root", "//", "/toto", true},
		{"root-slash unclean root with dot", "/.", "/toto", true},

		// --- normal, non-root path ---
		{"exact match", "/repo", "/repo", true},
		{"top-level child", "/repo", "/repo/toto", true},
		{"nested child", "/repo", "/repo/toto/titi.txt", true},
		{"child with trailing slash", "/repo", "/repo/", true},
		{"child with duplicated slash separator", "/repo", "/repo//toto", true},

		// --- sibling / prefix confusion (the classic path-traversal-style bug) ---
		{"sibling with same string prefix, longer", "/repo", "/repox", false},
		{"sibling with same string prefix, longer dir", "/repo", "/repoo/toto", false},
		{"sibling with same string prefix, shorter", "/repo", "/rep", false},
		{"unrelated absolute path", "/repo", "/other", false},
		{"parent of root", "/repo", "/", false},
		{"grandparent of root", "/repo/sub", "/repo", false},

		// --- root with trailing slash should be normalized via path.Clean ---
		{"root trailing slash, exact", "/repo/", "/repo", true},
		{"root trailing slash, child", "/repo/", "/repo/toto", true},
		{"root trailing slash, sibling rejected", "/repo/", "/repox", false},

		// --- root with redundant/unclean segments ---
		{"root with dot segment", "/repo/./sub", "/repo/sub", true},
		{"root with double slashes", "/repo//sub", "/repo/sub/x", true},
		{"root with dot-dot collapsing to parent", "/repo/sub/..", "/repo/x", true},

		// --- case sensitivity: comparison is literal, not case-insensitive ---
		{"case mismatch rejected", "/Repo", "/repo/x", false},
		{"case mismatch exact rejected", "/Repo", "/repo", false},

		// --- relative / malformed roots (defensive, should never authorize escape) ---
		{"empty root and empty joined", "", "", true},
		{"empty root, absolute joined", "", "/foo", false},
		{"relative root, absolute joined", "repo", "/repo", false},
		{"absolute root, relative joined", "/repo", "repo", false},
		{"dot root, absolute joined", ".", "/foo", false},

		// --- joined path not itself cleaned; cleanJoined normalizes it (defense in depth) ---
		{"joined containing dot-dot resolves back inside root", "/a/b", "/a/b/../c", false},
		{"joined containing dot-dot resolves to root itself", "/a/b", "/a/b/../b", true},
		{"joined escaping via dot-dot lexically outside prefix check", "/a/b", "/a/../c", false},

		// --- deeply nested paths ---
		{"deeply nested child", "/a/b/c", "/a/b/c/d/e/f/g.txt", true},
		{"deeply nested unrelated sibling", "/a/b/c", "/a/b/cc/d/e/f/g.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isContained(tt.root, tt.joined); got != tt.want {
				t.Errorf("isContained(%q, %q) = %v; want %v", tt.root, tt.joined, got, tt.want)
			}
		})
	}
}

// TestExport_SkipPermissions covers the skip_permissions knob against both
// the writeAtomic (file) and permissions (directory) paths: by default the
// special bits recorded in the snapshot are preserved, and with
// skip_permissions set, chmod never runs so they aren't applied.
func TestExport_SkipPermissions(t *testing.T) {
	cases := []struct {
		name            string
		isDir           bool
		skipPermissions bool
	}{
		{name: "file preserves setuid/setgid by default", isDir: false, skipPermissions: false},
		{name: "file skips chmod when skip_permissions is set", isDir: false, skipPermissions: true},
		{name: "directory preserves setgid by default", isDir: true, skipPermissions: false},
		{name: "directory skips chmod when skip_permissions is set", isDir: true, skipPermissions: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t)
			if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
				t.Fatalf("mkdir /repo: %v", err)
			}

			s := ts.newTestExportSftp(t, "/repo")
			s.skipPermissions = tc.skipPermissions
			records := make(chan *connectors.Record, 16)
			results, wait := runExporter(t, s, records)

			pathname := "/repo/file.txt"
			specialBits := os.ModeSetuid | os.ModeSetgid
			if tc.isDir {
				pathname = "/repo/dir"
				records <- connectors.NewRecord("/dir", "", objects.FileInfo{Lmode: os.ModeDir | os.ModeSetgid | 0750}, nil, nil)
			} else {
				content := []byte("setuid test")
				records <- connectors.NewRecord("/file.txt", "",
					objects.FileInfo{Lmode: os.ModeSetuid | os.ModeSetgid | 0755},
					nil,
					func() (io.ReadCloser, error) {
						return io.NopCloser(bytes.NewReader(content)), nil
					},
				)
			}
			close(records)

			got := drainResults(results)
			require.NoError(t, wait())
			require.Len(t, got, 1)
			assert.NoError(t, got[0].Err)

			info, err := ts.client.Stat(pathname)
			require.NoError(t, err)
			require.Equal(t, !tc.skipPermissions, info.Mode()&specialBits != 0,
				"special bits present, mode %v", info.Mode())
		})
	}
}

// TestExport_DirectorySetgidSurvivesChownOrdering is a regression test for
// the ordering of chown vs chmod in permissions().  
// chown clears the setgid bit even when chowning to the *same* uid/gid the file
// already has (verified empirically: chmod g+s, then chown $(id -u):$(id
// -g) on an unprivileged process strips the setgid bit). This means:
//
//   - chmod (restoring setgid) THEN chown -> setgid is stripped by the
//     chown call, even though setOwner asked for it.
//   - chown THEN chmod (restoring setgid) -> setgid survives, since nothing
//     runs after the chmod to strip it.
//
// This test does not require root: it chowns to the current process's own
// uid/gid, which is enough to trigger the kernel's clearing behaviour.
func TestExport_DirectorySetgidSurvivesChownOrdering(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() {
		_ = os.Chmod(ts.realPath("/repo/dir"), 0750)
	})
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	s.setOwner = true
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	uid := uint64(os.Getuid())
	gid := uint64(os.Getgid())
	records <- connectors.NewRecord("/dir", "",
		objects.FileInfo{Lmode: os.ModeDir | os.ModeSetgid | 0750, Luid: uid, Lgid: gid},
		nil, nil)
	close(records)

	got := drainResults(results)
	require.NoError(t, wait())
	require.Len(t, got, 1)
	assert.NoError(t, got[0].Err)

	info, err := ts.client.Stat("/repo/dir")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0750)|os.ModeDir|os.ModeSetgid, info.Mode(),
		"setgid bit must survive when both chown and chmod(setgid) are requested: chown must be applied before chmod")
}

// TestExport_FileSetuidSurvivesSetOwner is the file counterpart of
// TestExport_DirectorySetgidSurvivesChownOrdering: chown clears setuid and
// setgid on a regular file too, so with set_owner the file must be chowned
// before its mode is applied, or the restored file silently loses them.
func TestExport_FileSetuidSurvivesSetOwner(t *testing.T) {
	cases := []struct {
		name   string
		lnlink uint16
	}{
		{name: "regular file", lnlink: 1},
		{name: "hardlinked file", lnlink: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t)
			require.NoError(t, os.MkdirAll(ts.realPath("/repo"), 0750))

			s := ts.newTestExportSftp(t, "/repo")
			s.setOwner = true
			records := make(chan *connectors.Record, 16)
			results, wait := runExporter(t, s, records)

			content := []byte("setuid test")
			records <- connectors.NewRecord("/file.bin", "",
				objects.FileInfo{
					Lmode:  os.ModeSetuid | os.ModeSetgid | 0755,
					Luid:   uint64(os.Getuid()),
					Lgid:   uint64(os.Getgid()),
					Lnlink: tc.lnlink,
				},
				nil,
				func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(content)), nil
				},
			)
			close(records)

			got := drainResults(results)
			require.NoError(t, wait())
			require.Len(t, got, 1)
			require.NoError(t, got[0].Err)

			info, err := ts.client.Stat("/repo/file.bin")
			require.NoError(t, err)
			assert.Equal(t, os.ModeSetuid|os.ModeSetgid|0755, info.Mode(),
				"setuid/setgid must survive when set_owner is set: chown must be applied before chmod")
		})
	}
}
