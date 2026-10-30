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

package integration

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PlakarKorp/integrations/smb/exporter"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testDirs atomic.Int64

// testDir returns a share-relative directory no other test uses, created on
// the server.
func testDir(t *testing.T) string {
	t.Helper()
	dir := fmt.Sprintf("/%s-%d", strings.ReplaceAll(t.Name(), "/", "_"), testDirs.Add(1))
	sh(t, "mkdir -p "+shareDir+dir)
	return dir
}

func dirRecord(pathname string) *connectors.Record {
	return connectors.NewRecord(pathname, "", objects.FileInfo{Lmode: os.ModeDir | 0o755}, nil, nil)
}

func fileRecord(pathname, content string) *connectors.Record {
	fi := objects.FileInfo{Lsize: int64(len(content)), Lmode: 0o644}
	return connectors.NewRecord(pathname, "", fi, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	})
}

// export restores recs below root on the server and returns the per-record
// errors, keyed by pathname.
func export(t *testing.T, addr, root string, recs ...*connectors.Record) map[string]error {
	t.Helper()
	ctx := t.Context()

	exp, err := exporter.NewExporter(ctx, &connectors.Options{MaxConcurrency: 1}, "smb", map[string]string{
		"location": "smb://" + addr + "/" + share + root,
		"username": username,
		"password": password,
	})
	require.NoError(t, err)
	defer func() { _ = exp.Close(ctx) }()

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	errc := make(chan error, 1)
	go func() { errc <- exp.Export(ctx, records, results) }()
	go func() {
		defer close(records)
		for _, r := range recs {
			records <- r
		}
	}()

	errs := map[string]error{}
	for res := range results {
		errs[res.Record.Pathname] = res.Err
	}
	require.NoError(t, <-errc)
	return errs
}

func TestExportWritesTree(t *testing.T) {
	addr := server(t)
	dir := testDir(t)

	errs := export(t, addr, dir,
		dirRecord("/"),
		dirRecord("/sub"),
		fileRecord("/a.txt", "alpha"),
		fileRecord("/sub/b.txt", "bravo"),
		fileRecord("/deep/er/c.txt", "charlie"),
	)
	for name, err := range errs {
		assert.NoError(t, err, name)
	}

	assert.Equal(t, "alpha", sh(t, "cat "+shareDir+dir+"/a.txt"))
	assert.Equal(t, "bravo", sh(t, "cat "+shareDir+dir+"/sub/b.txt"))
	assert.Equal(t, "charlie", sh(t, "cat "+shareDir+dir+"/deep/er/c.txt"))
}

// PlakarKorp/security#50: the share holds <root>/keys -> ../.ssh and the
// snapshot carries /keys/authorized_keys. Samba resolves the link, so without
// containment the write lands in <share>/.ssh/authorized_keys.
func TestExportRefusesWriteBelowPreExistingSymlink(t *testing.T) {
	addr := server(t)
	dir := testDir(t)
	sh(t, "mkdir -p "+shareDir+dir+"/.ssh && echo -n original > "+shareDir+dir+"/.ssh/authorized_keys"+
		" && mkdir "+shareDir+dir+"/backup-restore && ln -s ../.ssh "+shareDir+dir+"/backup-restore/keys")

	root := dir + "/backup-restore"
	errs := export(t, addr, root,
		dirRecord("/"),
		dirRecord("/keys"),
		fileRecord("/keys/authorized_keys", "attacker"),
	)
	assert.NoError(t, errs["/"])
	assert.Error(t, errs["/keys"], "mkdir over a pre-existing symlink was accepted")
	assert.Error(t, errs["/keys/authorized_keys"], "write below a pre-existing symlink was accepted")
	assert.Equal(t, "original", sh(t, "cat "+shareDir+dir+"/.ssh/authorized_keys"), "the write escaped the restore root")

	// Without the directory record the parent is created on the way, and
	// must be refused the same.
	errs = export(t, addr, root, fileRecord("/keys/authorized_keys", "attacker"))
	assert.Error(t, errs["/keys/authorized_keys"])
	assert.Equal(t, "original", sh(t, "cat "+shareDir+dir+"/.ssh/authorized_keys"))
}

// A dangling symlink at the root is not followed into existence either.
func TestExportRefusesDanglingSymlinkAtRoot(t *testing.T) {
	addr := server(t)
	dir := testDir(t)
	sh(t, "mkdir -p "+shareDir+dir+"/.ssh "+shareDir+dir+"/backup-restore"+
		" && ln -s ../.ssh/authorized_keys "+shareDir+dir+"/backup-restore/authorized_keys"+
		" && ln -s ../.ssh/keys "+shareDir+dir+"/backup-restore/keys")

	errs := export(t, addr, dir+"/backup-restore",
		dirRecord("/"),
		fileRecord("/authorized_keys", "attacker"),
		dirRecord("/keys"),
		fileRecord("/keys/id", "attacker"),
	)
	assert.NoError(t, errs["/"])
	for _, name := range []string{"/authorized_keys", "/keys", "/keys/id"} {
		assert.Error(t, errs[name], "%s was restored through a dangling symlink", name)
	}
	assert.False(t, exists(t, dir+"/.ssh/authorized_keys"), "the dangling link's target was created")
	assert.False(t, exists(t, dir+"/.ssh/keys"), "the dangling link's target was created")
}

// What is already in the destination is not this restore's to overwrite.
func TestExportRefusesPreExistingEntries(t *testing.T) {
	addr := server(t)
	dir := testDir(t)
	sh(t, "mkdir "+shareDir+dir+"/old && echo -n old > "+shareDir+dir+"/a.txt")

	errs := export(t, addr, dir,
		dirRecord("/"),
		dirRecord("/old"),
		fileRecord("/old/x.txt", "new"),
		fileRecord("/a.txt", "new"),
		fileRecord("/b.txt", "new"),
	)
	assert.NoError(t, errs["/"])
	assert.Error(t, errs["/old"])
	assert.Error(t, errs["/old/x.txt"])
	assert.Error(t, errs["/a.txt"])
	assert.NoError(t, errs["/b.txt"])
	assert.Equal(t, "old", sh(t, "cat "+shareDir+dir+"/a.txt"), "an existing file was truncated")
	assert.False(t, exists(t, dir+"/old/x.txt"))
	assert.Equal(t, "new", sh(t, "cat "+shareDir+dir+"/b.txt"))
}
