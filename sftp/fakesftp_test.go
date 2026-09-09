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

// This file implements an in-process, disk-backed fake SFTP server used by
// the test-suite. It is intentionally NOT backed by pkg/sftp's own
// InMemHandler(), because that in-memory filesystem has simplified
// semantics (no real permission bits, no real ownership, no real symlink
// resolution against the OS) which makes it unsuitable for a subset of
// regression tests (path traversal, symlink escape, permission/ownership bypass).
//
// Instead, every SFTP path is translated onto a real temporary directory on
// disk via filepath.Join, and every translated path is verified to still be
// contained within that temp directory before any os.* call is made.
//
// The client and server are wired together with net.Pipe(), so there is no
// real network I/O, no ssh handshake, and no external process involved.
//
//	                      testharness_test.go — testServer
//	                     (bundles everything a test needs)
//	   ┌─────────────────────────────────────────────────────────────┐
//	   │                                                              │
//	   │   *sftp.Client            in-memory pipe            *sftp.RequestServer
//	   │  ┌──────────┐        net.Pipe()        ┌──────┐     ┌──────────────────┐
//	   │  │  client  │◄────────────────────────►│ conn │◄───►│       srv        │
//	   │  └──────────┘   clientConn   serverConn└──────┘     └────────┬─────────┘
//	   │  testServer.client                                           │
//	   │  (created in newTestServer,                                  │
//	   │   returned by sftp.NewClientPipe)                             │
//	   │       ▲                                                      │
//	   │       │ used directly by tests,                              │ dispatches
//	   │       │ or via newTestSftp()                                 │ Get/Put/Cmd/List
//	   │       │ (see newTestSftp, below)                              │ (see sftp.Handlers
//	   │       │                                                      │  wiring in
//	   │       │                                                      │  newTestServer)
//	   │       ▼                                                      ▼
//	   │  ┌──────────┐                                    ┌────────────────────┐
//	   │  │   Sftp   │                                    │ localRootHandlers  │
//	   │  │(connector│                                    │  (implements the   │
//	   │  │  under   │                                    │  sftp.Handlers     │
//	   │  │  test,   │                                    │  sub-interfaces,   │
//	   │  │  in the   │                                    │  defined at the    │
//	   │  │ package's │                                    │  top of this file) │
//	   │  │ own       │                                    │                    │
//	   │  │ connector.go)│                                 │                    │
//	   │  └──────────┘                                    └──────────┬─────────┘
//	   │                                                              │ h.real(path)
//	   │                                                              │ (localRootHandlers.real,
//	   │                                                              │  defined just below)
//	   │                                                              ▼
//	   │                                                    ┌────────────────────┐
//	   │                                                    │   root (string)    │
//	   │                                                    │ localRootHandlers  │
//	   │                                                    │      .root         │
//	   │                                                    │ = a t.TempDir(),   │
//	   │                                                    │ i.e. a REAL dir    │
//	   │                                                    │ on the real OS     │
//	   │                                                    │ filesystem.        │
//	   │                                                    │ (see also          │
//	   │                                                    │  testServer.root   │
//	   │                                                    │  and realPath,     │
//	   │                                                    │  at the bottom of  │
//	   │                                                    │  this file)        │
//	   │                                                    └────────────────────┘
//	   └──────────────────────────────────────────────────────────────┘
//
// In short: test code talks to ts.client (or an *Sftp connector built via
// ts.newTestSftp) exactly as if it were a real SFTP server. Every request
// travels over the in-memory pipe to localRootHandlers, which performs the
// equivalent real os.* syscall against a sandboxed temp directory. Tests
// that need to inspect ground truth can use ts.realPath(p) to get the real
// on-disk path and assert on it directly (e.g. via os.Stat/os.Lstat).

import (
	"errors"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

// localRootHandlers implements the sftp.Handlers interfaces (FileReader,
// FileWriter, FileCmder, FileLister, LstatFileLister, ReadlinkFileLister)
// on top of a real directory on disk. All paths received from the SFTP
// client are translated relative to root via the real method, so the
// backing store behaves like a genuine (if minimal) SFTP server rooted at
// an arbitrary directory.
type localRootHandlers struct {
	root string // real, absolute path on disk that acts as "/" for the fake server
}

func (h *localRootHandlers) real(p string) (string, error) {
	clean := path.Clean("/" + p)
	full := filepath.Join(h.root, filepath.FromSlash(clean))

	// Guard against path escape: full must either be h.root itself, or a
	// path nested under h.root (h.root + separator + ...). Without this
	// check, a cleaned path like "/../../etc/passwd" would still resolve
	// (via filepath.Join's own cleaning) to somewhere outside h.root, and
	// leak access to the real filesystem beyond the sandboxed test root.
	if full != h.root && !strings.HasPrefix(full, h.root+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return full, nil
}

// Fileread opens the requested file for reading. It is invoked by the SFTP
// request server for the "Get" method (i.e. when a client downloads a
// file).
func (h *localRootHandlers) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	p, err := h.real(r.Filepath)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// Filewrite opens (and, depending on flags, creates/truncates) the
// requested file for writing. It is invoked for the "Put" and "Open"
// methods. The SFTP protocol's open flags (Creat/Trunc/Excl/Append) are
// translated one-to-one into the equivalent os.O_* flags.
func (h *localRootHandlers) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	p, err := h.real(r.Filepath)
	if err != nil {
		return nil, err
	}

	flags := os.O_WRONLY
	pflags := r.Pflags()
	if pflags.Creat {
		flags |= os.O_CREATE
	}
	if pflags.Trunc {
		flags |= os.O_TRUNC
	}
	if pflags.Excl {
		flags |= os.O_EXCL
	}
	if pflags.Append {
		flags |= os.O_APPEND
	}

	return os.OpenFile(p, flags, 0644)
}

// Filecmd handles all the SFTP "command" methods that mutate the
// filesystem but do not stream file data: Setstat, Rename, PosixRename,
// Rmdir, Mkdir, Remove, Link and Symlink. r.Filepath is the primary path
// operated on; r.Target (when present) is the secondary path used by
// rename/link/symlink operations.
func (h *localRootHandlers) Filecmd(r *sftp.Request) error {
	p, err := h.real(r.Filepath)
	if err != nil {
		return err
	}

	switch r.Method {
	// Setstat applies attribute changes (mode, ownership, size) sent by
	// the client, e.g. via sftp.Client.Chmod/Chown/Truncate.
	case "Setstat":
		attrs := r.Attributes()
		flags := r.AttrFlags()
		if flags.Permissions {
			if err := os.Chmod(p, attrs.FileMode().Perm()); err != nil {
				return err
			}
		}
		if flags.UidGid {
			_ = os.Chown(p, int(attrs.UID), int(attrs.GID))
		}
		if flags.Size {
			return os.Truncate(p, int64(attrs.Size))
		}
		return nil

	// Rename implements the classic SFTP rename, which (per spec) must
	// fail if the destination already exists - hence the explicit
	// os.Lstat existence check before renaming.
	case "Rename":
		target, err := h.real(r.Target)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(target); err == nil {
			return os.ErrExist
		}
		return os.Rename(p, target)

	// PosixRename is the posix-rename@openssh.com extension: unlike
	// plain Rename, it is allowed to silently replace an existing
	// destination (same semantics as os.Rename/POSIX rename(2)).
	case "PosixRename":
		target, err := h.real(r.Target)
		if err != nil {
			return err
		}
		return os.Rename(p, target)

	// Rmdir removes an empty directory.
	case "Rmdir":
		return os.Remove(p)

	// Mkdir creates a new directory with a fixed, non-restrictive mode;
	// tests that care about specific directory permissions should chmod
	// afterwards via Setstat.
	case "Mkdir":
		return os.Mkdir(p, 0700)

	// Remove deletes a single file (not a directory).
	case "Remove":
		return os.Remove(p)

	// Link creates a hard link at r.Target pointing to the file at
	// r.Filepath.
	case "Link":
		target, err := h.real(r.Target)
		if err != nil {
			return err
		}
		return os.Link(p, target)

	// Symlink creates a symbolic link. Per pkg/sftp's request semantics,
	// r.Filepath carries the link *target* (the raw string stored inside
	// the symlink, not necessarily resolved/validated against h.root),
	// while r.Target carries the *link path* to create - so only the
	// link path is translated via h.real, matching real sftp-server
	// behaviour of storing symlink targets verbatim.
	case "Symlink":
		linkpath, err := h.real(r.Target)
		if err != nil {
			return err
		}
		return os.Symlink(r.Filepath, linkpath)
	}

	return errors.ErrUnsupported
}

// Filelist handles directory listing ("List") and stat ("Stat") requests,
// returning a sftp.ListerAt over the resulting os.FileInfo entries. Note
// that "Lstat" is handled separately by the Lstat method below.
func (h *localRootHandlers) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	p, err := h.real(r.Filepath)
	if err != nil {
		return nil, err
	}

	switch r.Method {
	// List returns the directory's entries, sorted by name for
	// deterministic test assertions.
	case "List":
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(entries))
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			infos = append(infos, info)
		}
		sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
		return listerAt(infos), nil

	// Stat follows symlinks (via os.Stat) and returns info about the
	// resolved target.
	case "Stat":
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		return listerAt([]os.FileInfo{info}), nil
	}

	return nil, errors.ErrUnsupported
}

// Lstat implements the sftp.LstatFileLister interface: like Stat, but does
// NOT follow symlinks (via os.Lstat), so a symlink itself is described
// rather than whatever it points to. This is what lets security regression
// tests observe real symlink metadata (os.ModeSymlink, target, etc.).
func (h *localRootHandlers) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	p, err := h.real(r.Filepath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	return listerAt([]os.FileInfo{info}), nil
}

// Readlink implements the sftp.ReadlinkFileLister interface, returning the
// raw target string stored in the symlink at p (without resolving it).
func (h *localRootHandlers) Readlink(p string) (string, error) {
	real, err := h.real(p)
	if err != nil {
		return "", err
	}
	return os.Readlink(real)
}

// listerAt is a trivial slice-backed implementation of sftp.ListerAt, used
// to return one or more os.FileInfo entries (for List/Stat/Lstat) to the
// request server, which paginates through it via ListAt.
type listerAt []os.FileInfo

// ListAt copies as many entries as fit into dst, starting at offset, and
// mirrors io.ReaderAt semantics: it returns io.EOF once there are no more
// entries to hand back, even alongside a partial/full copy.
func (l listerAt) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(dst, l[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

// testServer bundles together a running in-process fake SFTP server, the
// client wired to it, and the real directory on disk backing it.
type testServer struct {
	client *sftp.Client // client connected to the fake server, ready to use
	root   string       // real path on disk that the fake server treats as "/"
}

// newTestServer starts an in-process, disk-backed fake SFTP server rooted
// at a fresh t.TempDir(), connects an *sftp.Client to it over an in-memory
// net.Pipe() (no real network I/O, no ssh handshake, no external process),
// and registers a t.Cleanup to shut both down when the test finishes.
func newTestServer(t *testing.T) *testServer {
	t.Helper()

	root := t.TempDir()
	handlers := &localRootHandlers{root: root}

	// net.Pipe gives us two connected, in-memory net.Conn ends: one plays
	// the role of the client's transport, the other the server's.
	clientConn, serverConn := net.Pipe()

	// The request server dispatches each of the four handler interfaces
	// (Get/Put/Cmd/List) to our single localRootHandlers implementation.
	srv := sftp.NewRequestServer(serverConn, sftp.Handlers{
		FileGet:  handlers,
		FilePut:  handlers,
		FileCmd:  handlers,
		FileList: handlers,
	})

	// Serve blocks until the connection is closed, so it must run in its
	// own goroutine; its final error is captured so Cleanup can wait for
	// a clean shutdown instead of leaking the goroutine.
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve()
	}()

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatalf("newTestServer: NewClientPipe: %v", err)
	}

	t.Cleanup(func() {
		_ = client.Close()
		_ = srv.Close()
		<-serveErr
	})

	return &testServer{client: client, root: root}
}

// newTestSftp constructs an *Sftp connector wired directly to this fake
// server's client, bypassing connect()/ensureMaster() entirely (those
// require a real ssh/sftp-server pair). rootDir is the SFTP-visible root
// ("/"-style path) the connector should operate against; it defaults to
// "/" when empty, and must correspond to a real directory under ts.root.
func (ts *testServer) newTestSftp(t *testing.T, rootDir string) *Sftp {
	t.Helper()

	if rootDir == "" {
		rootDir = "/"
	}

	return &Sftp{
		maxConcurrency: 1,
		client:         ts.client,
		rootDir:        rootDir,
	}
}

// realPath returns the real on-disk path corresponding to the given
// SFTP-visible path p, for use by tests that want to assert on real
// filesystem state (permissions, ownership, symlink targets, ...)
// alongside the connector's/client's own view of that path.
func (ts *testServer) realPath(p string) string {
	clean := path.Clean("/" + p)
	return filepath.Join(ts.root, filepath.FromSlash(clean))
}

// realStat is a convenience wrapper around os.Stat(ts.realPath(p)), for
// tests that want to assert on real filesystem metadata (mode, size,
// IsDir, ...) for a given SFTP-visible path without having to compute the
// real path themselves first. Like os.Stat, it follows symlinks; use
// os.Lstat(ts.realPath(p)) directly when the symlink itself (rather than
// its target) needs to be inspected.
func (ts *testServer) realStat(p string) (os.FileInfo, error) {
	return os.Stat(ts.realPath(p))
}

// TestHarnessRoundtrip is a smoke test proving the fake in-process SFTP
// server works end to end: write a file through the client, read it back,
// list the directory, stat it, chmod it, symlink it, and read the link
// back - all validated against both the SFTP client's view and the real
// file on disk.
func TestHarnessRoundtrip(t *testing.T) {
	ts := newTestServer(t)

	f, err := ts.client.Create("/hello.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte("hello world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Validate against the real file on disk.
	data, err := os.ReadFile(ts.realPath("/hello.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("unexpected file contents: %q", data)
	}

	// Read back through the client.
	rf, err := ts.client.Open("/hello.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rf.Close()
	buf := make([]byte, 32)
	n, _ := rf.Read(buf)
	if string(buf[:n]) != "hello world" {
		t.Fatalf("unexpected read contents: %q", buf[:n])
	}

	// Mkdir + List.
	if err := ts.client.Mkdir("/dir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	entries, err := ts.client.ReadDir("/")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["hello.txt"] || !names["dir"] {
		t.Fatalf("unexpected directory listing: %v", names)
	}

	// Chmod through the client, validate against real filesystem mode
	// bits (proves permission semantics are real, not virtual).
	if err := ts.client.Chmod("/hello.txt", 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	info, err := os.Stat(ts.realPath("/hello.txt"))
	if err != nil {
		t.Fatalf("os.Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected real mode 0600, got %v", info.Mode().Perm())
	}

	// Symlink + Readlink round trip.
	if err := ts.client.Symlink("/hello.txt", "/link.txt"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	target, err := ts.client.ReadLink("/link.txt")
	if err != nil {
		t.Fatalf("ReadLink: %v", err)
	}
	if target != "/hello.txt" {
		t.Fatalf("unexpected symlink target: %q", target)
	}
	realTarget, err := os.Readlink(ts.realPath("/link.txt"))
	if err != nil {
		t.Fatalf("os.Readlink: %v", err)
	}
	if realTarget != "/hello.txt" {
		t.Fatalf("unexpected real symlink target: %q", realTarget)
	}

	// Path escape attempts must fail, not touch anything outside root.
	if _, err := ts.client.Stat("/../../etc/passwd"); err == nil {
		t.Fatalf("expected path escape to fail")
	}

	// Connector construction bypasses connect()/ensureMaster().
	conn := ts.newTestSftp(t, "/")
	if conn.Root() != "/" {
		t.Fatalf("unexpected root: %q", conn.Root())
	}
	if _, err := conn.client.Lstat(filepath.Join("/", "hello.txt")); err != nil {
		t.Fatalf("connector client Lstat: %v", err)
	}
}
