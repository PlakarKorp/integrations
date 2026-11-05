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

package exporter

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeShare is an in-memory SMB share, keyed by clean "/abs" paths. With
// reparse false it behaves like a Samba without reparse point support:
// symlinks are resolved on every operation and never reported. With reparse
// true it behaves like a Windows server: Lstat reports them, but a create
// still goes through a symlink in the final component, a dangling one
// included, and lands on its target.
type fakeShare struct {
	reparse bool
	entries map[string]*fakeEntry
}

type fakeEntry struct {
	mode    os.FileMode
	target  string
	content bytes.Buffer
}

func newFakeShare(reparse bool) *fakeShare {
	return &fakeShare{reparse: reparse, entries: map[string]*fakeEntry{"/": {mode: os.ModeDir | 0o755}}}
}

func (f *fakeShare) dir(p string) { f.entries[p] = &fakeEntry{mode: os.ModeDir | 0o755} }
func (f *fakeShare) symlink(p, target string) {
	f.entries[p] = &fakeEntry{mode: os.ModeSymlink | 0o777, target: target}
}
func (f *fakeShare) file(p, content string) {
	e := &fakeEntry{mode: 0o644}
	e.content.WriteString(content)
	f.entries[p] = e
}

func (f *fakeShare) content(p string) string {
	if e := f.entries[p]; e != nil {
		return e.content.String()
	}
	return ""
}

// resolve walks p the way the server would, following symlinks in
// intermediate components and, when followLast is set, in the final one. It
// returns the path the operation lands on; that path may not exist yet.
func (f *fakeShare) resolve(p string, followLast bool) (string, error) {
	comps := strings.Split(strings.Trim(path.Clean(p), "/"), "/")
	cur := "/"
	for hops := 0; len(comps) > 0; comps = comps[1:] {
		if comps[0] == "" {
			continue
		}
		next := path.Join(cur, comps[0])
		e := f.entries[next]
		last := len(comps) == 1
		switch {
		case e == nil:
			if !last {
				return "", os.ErrNotExist
			}
			return next, nil
		case e.mode&os.ModeSymlink != 0 && (!last || followLast):
			if hops++; hops > 8 {
				return "", errors.New("too many levels of symbolic links")
			}
			target := e.target
			if !path.IsAbs(target) {
				target = path.Join(cur, target)
			}
			rest := strings.Join(comps[1:], "/")
			comps = append([]string{""}, strings.Split(strings.Trim(path.Join(target, rest), "/"), "/")...)
			cur = "/"
			continue
		case !last && !e.mode.IsDir():
			return "", os.ErrNotExist
		}
		cur = next
	}
	return cur, nil
}

type fakeInfo struct {
	name string
	mode os.FileMode
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return 0 }
func (i fakeInfo) Mode() os.FileMode  { return i.mode }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeInfo) Sys() any           { return nil }

func (f *fakeShare) Lstat(p string) (os.FileInfo, error) {
	r, err := f.resolve(p, !f.reparse)
	if err != nil {
		return nil, err
	}
	e := f.entries[r]
	if e == nil {
		return nil, os.ErrNotExist
	}
	return fakeInfo{name: path.Base(r), mode: e.mode}, nil
}

// create places a new entry at p. A Windows server follows a final symlink
// (mkdir and open both reparse to its target); a Samba creates with O_EXCL
// semantics and fails on the link itself.
func (f *fakeShare) create(p string, e *fakeEntry) (string, error) {
	r, err := f.resolve(p, f.reparse)
	if err != nil {
		return "", err
	}
	if f.entries[r] != nil {
		return "", os.ErrExist
	}
	f.entries[r] = e
	return r, nil
}

func (f *fakeShare) Mkdir(p string, perm os.FileMode) error {
	_, err := f.create(p, &fakeEntry{mode: os.ModeDir | perm})
	return err
}

func (f *fakeShare) MkdirAll(p string, perm os.FileMode) error {
	r, err := f.resolve(p, true)
	if err != nil {
		return err
	}
	for dir := r; f.entries[dir] == nil; dir = path.Dir(dir) {
		f.entries[dir] = &fakeEntry{mode: os.ModeDir | perm}
	}
	return nil
}

type fakeWriter struct{ e *fakeEntry }

func (w fakeWriter) Write(b []byte) (int, error) { return w.e.content.Write(b) }
func (w fakeWriter) Close() error                { return nil }

func (f *fakeShare) Create(p string) (io.WriteCloser, error) {
	e := &fakeEntry{mode: 0o644}
	if _, err := f.create(p, e); err != nil {
		return nil, err
	}
	return fakeWriter{e}, nil
}

func (f *fakeShare) Symlink(target, linkpath string) error {
	r, err := f.resolve(linkpath, false)
	if err != nil {
		return err
	}
	if f.entries[r] != nil {
		return os.ErrExist
	}
	f.entries[r] = &fakeEntry{mode: os.ModeSymlink | 0o777, target: target}
	return nil
}

func (f *fakeShare) Chtimes(p string, mtime time.Time) error { return nil }
func (f *fakeShare) Close() error                            { return nil }

func dirRecord(pathname string) *connectors.Record {
	return connectors.NewRecord(pathname, "", objects.FileInfo{Lmode: os.ModeDir | 0o755}, nil, nil)
}

func fileRecord(pathname, content string) *connectors.Record {
	fi := objects.FileInfo{Lsize: int64(len(content)), Lmode: 0o644}
	return connectors.NewRecord(pathname, "", fi, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	})
}

func symlinkRecord(pathname, target string) *connectors.Record {
	return connectors.NewRecord(pathname, target, objects.FileInfo{Lmode: os.ModeSymlink | 0o777}, nil, nil)
}

// export restores recs into root on fs and returns the per-record errors,
// keyed by pathname.
func export(t *testing.T, fs *fakeShare, root string, recs ...*connectors.Record) map[string]error {
	t.Helper()
	p := &Exporter{conn: fs, rootDir: root}

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	errc := make(chan error, 1)
	go func() { errc <- p.Export(t.Context(), records, results) }()
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

// servers runs fn against both server behaviours.
func servers(t *testing.T, fn func(t *testing.T, fs *fakeShare)) {
	for _, tc := range []struct {
		name    string
		reparse bool
	}{{"samba without reparse points", false}, {"server reporting reparse points", true}} {
		t.Run(tc.name, func(t *testing.T) { fn(t, newFakeShare(tc.reparse)) })
	}
}

func TestExportWritesTree(t *testing.T) {
	fs := newFakeShare(true)
	fs.dir("/restore")

	// The root itself is missing: the root record creates it.
	errs := export(t, fs, "/restore/new",
		dirRecord("/"),
		dirRecord("/sub"),
		fileRecord("/a.txt", "alpha"),
		fileRecord("/sub/b.txt", "bravo"),
		symlinkRecord("/sub/l", "b.txt"),
		// no directory record for the parents: they are created on the way
		fileRecord("/deep/er/c.txt", "charlie"),
		// a directory record arriving after its subtree is still ours
		dirRecord("/deep"),
	)
	for name, err := range errs {
		assert.NoError(t, err, name)
	}

	assert.Equal(t, "alpha", fs.content("/restore/new/a.txt"))
	assert.Equal(t, "bravo", fs.content("/restore/new/sub/b.txt"))
	assert.Equal(t, "charlie", fs.content("/restore/new/deep/er/c.txt"))
	if assert.NotNil(t, fs.entries["/restore/new/sub/l"]) {
		assert.Equal(t, "b.txt", fs.entries["/restore/new/sub/l"].target)
	}
}

// The reporter's scenario (PlakarKorp/security#50 and #60): the share holds
// backup-restore/keys -> ../.ssh, the restore root is /backup-restore and the
// snapshot carries /keys/authorized_keys. The server resolves the link, so
// the write would land in <share>/.ssh/authorized_keys.
func TestExportRefusesWriteBelowPreExistingSymlink(t *testing.T) {
	servers(t, func(t *testing.T, fs *fakeShare) {
		fs.dir("/.ssh")
		fs.file("/.ssh/authorized_keys", "original")
		fs.dir("/backup-restore")
		fs.symlink("/backup-restore/keys", "../.ssh")

		errs := export(t, fs, "/backup-restore",
			dirRecord("/"),
			dirRecord("/keys"),
			fileRecord("/keys/authorized_keys", "attacker"),
		)
		assert.NoError(t, errs["/"])
		assert.Error(t, errs["/keys"], "mkdir over a pre-existing symlink was accepted")
		assert.Error(t, errs["/keys/authorized_keys"], "write below a pre-existing symlink was accepted")
		assert.Equal(t, "original", fs.content("/.ssh/authorized_keys"), "the write escaped the restore root")

		// Without the directory record the parent is created on the way, and
		// must be refused the same.
		errs = export(t, fs, "/backup-restore", fileRecord("/keys/authorized_keys", "attacker"))
		assert.Error(t, errs["/keys/authorized_keys"])
		assert.Equal(t, "original", fs.content("/.ssh/authorized_keys"))
	})
}

// A dangling symlink is invisible to a stat through a server that resolves
// links, and a Windows server creates the target of one on open: neither may
// turn a restore into a write elsewhere.
func TestExportRefusesDanglingSymlinkAtRoot(t *testing.T) {
	servers(t, func(t *testing.T, fs *fakeShare) {
		fs.dir("/.ssh")
		fs.dir("/backup-restore")
		fs.symlink("/backup-restore/authorized_keys", "../.ssh/authorized_keys")
		fs.symlink("/backup-restore/keys", "../.ssh/keys")

		errs := export(t, fs, "/backup-restore",
			dirRecord("/"),
			fileRecord("/authorized_keys", "attacker"),
			dirRecord("/keys"),
			fileRecord("/keys/id", "attacker"),
		)
		assert.NoError(t, errs["/"])
		for _, name := range []string{"/authorized_keys", "/keys", "/keys/id"} {
			assert.Error(t, errs[name], "%s was restored through a dangling symlink", name)
		}
		assert.Nil(t, fs.entries["/.ssh/authorized_keys"], "the dangling link's target was created")
		assert.Nil(t, fs.entries["/.ssh/keys"], "the dangling link's target was created")
	})
}

// Entries already present in the destination are not this restore's to
// overwrite: a pre-existing directory cannot be told from a symlink to one.
func TestExportRefusesPreExistingEntries(t *testing.T) {
	servers(t, func(t *testing.T, fs *fakeShare) {
		fs.dir("/restore")
		fs.dir("/restore/old")
		fs.file("/restore/a.txt", "old")

		errs := export(t, fs, "/restore",
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
		assert.Equal(t, "old", fs.content("/restore/a.txt"), "an existing file was truncated")
		assert.Nil(t, fs.entries["/restore/old/x.txt"])
		assert.Equal(t, "new", fs.content("/restore/b.txt"))
	})
}

func TestExportRefusesEscapingSymlinkTarget(t *testing.T) {
	fs := newFakeShare(true)
	fs.dir("/restore")

	errs := export(t, fs, "/restore",
		dirRecord("/"),
		dirRecord("/sub"),
		symlinkRecord("/sub/ok", "../sub"),
		symlinkRecord("/sub/escape", "../../etc"),
		symlinkRecord("/sub/abs", "/etc"),
	)
	assert.NoError(t, errs["/sub/ok"])
	assert.Error(t, errs["/sub/escape"])
	assert.Error(t, errs["/sub/abs"])
	assert.Nil(t, fs.entries["/restore/sub/escape"])
	assert.Nil(t, fs.entries["/restore/sub/abs"])
}

func TestFakeShareResolvesLikeAServer(t *testing.T) {
	fs := newFakeShare(false)
	fs.dir("/a")
	fs.dir("/a/b")
	fs.symlink("/l", "a")
	fs.symlink("/a/dangling", "nowhere")

	r, err := fs.resolve("/l/b/c", true)
	require.NoError(t, err)
	assert.Equal(t, "/a/b/c", r)

	r, err = fs.resolve("/a/dangling", false)
	require.NoError(t, err)
	assert.Equal(t, "/a/dangling", r)

	r, err = fs.resolve("/a/dangling", true)
	require.NoError(t, err)
	assert.Equal(t, "/a/nowhere", r)

	_, err = fs.resolve("/missing/x", true)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
