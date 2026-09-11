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
	"syscall"
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
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected *syscall.Stat_t from file info")
	assert.Equal(t, uid, uint64(stat.Uid), "file uid must match the record's Luid")
	assert.Equal(t, gid, uint64(stat.Gid), "file gid must match the record's Lgid")
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
