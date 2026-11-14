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

// Package common holds the NFS-specific plumbing shared by the importer and
// exporter: parsing the nfs:// location, dialing the MOUNT service, mounting
// an export and handing back a connected NFSv3 target rooted at that export.
//
// The connector speaks NFSv3 directly over ONC-RPC (no kernel mount, no root
// privileges) using github.com/vmware/go-nfs-client.
package common

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/vmware/go-nfs-client/nfs"
	"github.com/vmware/go-nfs-client/nfs/rpc"
	"github.com/vmware/go-nfs-client/nfs/xdr"
)

// Conn bundles a mounted NFS export and serializes access to it.
//
// The underlying go-nfs-client transport is NOT safe for concurrent RPCs: a
// request write and its reply read are locked separately, so interleaved calls
// trip the XID check and corrupt the stream. The lock is therefore held around
// every call into the client, but never across calls: an open file keeps its
// own filehandle and offset, so the RPCs of one transfer can interleave with
// other operations on the same connection. Every NFS access goes through the
// guarded methods on Conn; callers must not touch the raw target.
type Conn struct {
	mu     sync.Mutex
	mount  *nfs.Mount
	target *nfs.Target
	auth   rpc.Auth

	// Export is the server path that was mounted (the export point).
	Export string
}

// recoverErr turns a panic from the vendored RPC layer, which panics on
// truncated or rejected replies, into an error. Defer it in every method that
// enters the client.
func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("nfs: malformed server reply: %v", r)
	}
}

// Lookup resolves p relative to the export, returning its attributes (nil for
// the export root, which has no final component) and filehandle.
func (c *Conn) Lookup(p string) (attr *nfs.Fattr, fh []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	info, fh, err := c.target.Lookup(p)
	if err != nil {
		return nil, nil, err
	}
	attr, _ = info.(*nfs.Fattr)
	return attr, fh, nil
}

// ReadDirPlus lists dir with attributes in a single round-trip.
func (c *Conn) ReadDirPlus(dir string) (entries []*nfs.EntryPlus, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	return c.target.ReadDirPlus(dir)
}

// Readlink returns the target of the symlink at p.
func (c *Conn) Readlink(p string) (link string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	f, err := c.target.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return f.Readlink()
}

// Mkdir creates a directory at p with the given permissions.
func (c *Conn) Mkdir(p string, perm os.FileMode) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	_, err = c.target.Mkdir(p, perm)
	return err
}

// OpenReader opens the file at p for streaming reads. The connection lock is
// taken for each RPC only, so an open reader does not hold up other operations.
func (c *Conn) OpenReader(p string) (rc io.ReadCloser, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	f, err := c.target.Open(p)
	if err != nil {
		return nil, err
	}
	return &fileReader{conn: c, file: f, r: f}, nil
}

// OpenReaderRange is like OpenReader but starts at offset and reads at most
// length bytes. Used for partial (packfile blob) reads.
func (c *Conn) OpenReaderRange(p string, offset, length int64) (rc io.ReadCloser, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	f, err := c.target.Open(p)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &fileReader{conn: c, file: f, r: io.LimitReader(f, length)}, nil
}

// fileReader is an io.ReadCloser over an NFS file that takes the connection
// lock for each Read and for Close.
type fileReader struct {
	conn   *Conn
	file   *nfs.File
	r      io.Reader // the file itself, or a limited range of it
	closed bool
}

func (fr *fileReader) Read(p []byte) (n int, err error) {
	fr.conn.mu.Lock()
	defer fr.conn.mu.Unlock()
	defer recoverErr(&err)
	return fr.r.Read(p)
}

func (fr *fileReader) Close() (err error) {
	if fr.closed {
		return nil
	}
	fr.closed = true
	fr.conn.mu.Lock()
	defer fr.conn.mu.Unlock()
	defer recoverErr(&err)
	return fr.file.Close()
}

// writeChunk bounds how much of the caller's data is written per locked call,
// so other operations on the connection get a turn during a large transfer.
const writeChunk = 1 << 20

// WriteFile creates the file at p with the given permissions and streams r into
// it. Reading from r happens without the connection lock, so a slow producer
// does not hold up other operations on the connection.
func (c *Conn) WriteFile(p string, perm os.FileMode, r io.Reader) (err error) {
	f, err := c.createFile(p, perm)
	if err != nil {
		return err
	}

	buf := make([]byte, writeChunk)
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			if werr := c.writeLocked(f, buf[:n]); werr != nil {
				_ = c.closeLocked(f)
				return werr
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			_ = c.closeLocked(f)
			return rerr
		}
	}
	return c.closeLocked(f)
}

func (c *Conn) createFile(p string, perm os.FileMode) (f *nfs.File, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	// OpenFile does not truncate: a shorter file would keep the old tail.
	if _, _, lerr := c.target.Lookup(p); lerr == nil {
		if err := c.target.Remove(p); err != nil {
			return nil, err
		}
	}
	return c.target.OpenFile(p, perm)
}

func (c *Conn) writeLocked(f *nfs.File, p []byte) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	_, err = f.Write(p)
	return err
}

func (c *Conn) closeLocked(f *nfs.File) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	return f.Close()
}

// nfsProc3Rename is NFSv3's RENAME procedure number (RFC 1813 §3.3.14).
// go-nfs-client doesn't implement it, so Rename builds and dispatches the RPC
// by hand, reusing the library's exported transport (Target.Call) and XDR
// types: there is no unexported piece that needs forking the module for.
const nfsProc3Rename = 14

type rename3Args struct {
	rpc.Header
	From nfs.Diropargs3
	To   nfs.Diropargs3
}

// Rename renames oldPath to newPath, both resolved relative to the export.
// Callers use this to make writes atomic: write to a temp name, then Rename
// into place, so a reader never observes a partially written file.
func (c *Conn) Rename(oldPath, newPath string) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)

	oldDir, oldName := path.Split(oldPath)
	newDir, newName := path.Split(newPath)

	_, fromFH, err := c.target.Lookup(oldDir)
	if err != nil {
		return fmt.Errorf("lookup %q: %w", oldDir, err)
	}
	_, toFH, err := c.target.Lookup(newDir)
	if err != nil {
		return fmt.Errorf("lookup %q: %w", newDir, err)
	}

	res, err := c.target.Call(&rename3Args{
		Header: rpc.Header{
			Rpcvers: 2,
			Prog:    nfs.Nfs3Prog,
			Vers:    nfs.Nfs3Vers,
			Proc:    nfsProc3Rename,
			Cred:    c.auth,
			Verf:    rpc.AuthNull,
		},
		From: nfs.Diropargs3{FH: fromFH, Filename: oldName},
		To:   nfs.Diropargs3{FH: toFH, Filename: newName},
	})
	if err != nil {
		return err
	}

	// RENAME3res carries wcc_data for both directories on success; neither
	// caller here needs it, so it's left undecoded (same as this library's
	// own Remove/RmDir, which discard it too).
	status, err := xdr.ReadUint32(res)
	if err != nil {
		return err
	}
	return nfs.NFS3Error(status)
}

// Remove deletes the file at p.
func (c *Conn) Remove(p string) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&err)
	return c.target.Remove(p)
}

// Config is the parsed, normalized form of a connector configuration map.
type Config struct {
	// Host is the NFS server hostname or IP. The MOUNT and NFS services
	// are located through the server's portmapper on port 111; neither the
	// portmapper nor the service ports can be overridden.
	Host string
	// Export is the server-side export to mount, e.g. "/exports/data".
	Export string
	// Root is the path to walk, relative to the export ("/" for the whole
	// export).
	Root string
	// UID/GID are the AUTH_UNIX credentials presented to the server. NFS
	// authorization is done by the server against these numeric ids.
	UID uint32
	GID uint32
}

// ParseConfig turns a connector config map into a Config.
//
// The location is an nfs:// URL of the form:
//
//	nfs://<host>/<export>[/<subpath>]
//
// Everything in the URL path is treated as the export to mount; a "root"
// parameter (or a deeper subpath) narrows the walk within that export. This
// matches mount.nfs semantics, where the server hands out a filehandle for a
// path it explicitly exports.
//
// The export to mount can also be given out-of-band through the "export"
// parameter, which takes precedence over the URL path. This lets a UI expose
// the mountpoint as its own field instead of burying it in the location URL;
// when it is set the location may be a bare nfs://<host>.
//
// A port in the location or a "port" parameter is rejected: the client always
// goes through the portmapper on port 111 and dials the ports it returns, so
// a user-supplied port could not be honored and used to fail at dial time
// with a confusing error.
func ParseConfig(config map[string]string) (*Config, error) {
	target := config["location"]
	if target == "" {
		return nil, errors.New("missing location")
	}

	parsed, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("invalid location %q: %w", target, err)
	}
	if parsed.Scheme != "nfs" {
		return nil, fmt.Errorf("location scheme must be nfs://, got %q", parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing host in location %q", target)
	}

	if parsed.Port() != "" || config["port"] != "" {
		return nil, errors.New("port is not supported: the NFS and MOUNT services are located through the server's portmapper on port 111")
	}

	// The export field, when set, takes precedence over the URL path.
	export := cleanAbs(parsed.Path)
	if e := config["export"]; e != "" {
		export = cleanAbs(e)
	}
	if export == "/" || export == "" {
		return nil, errors.New("missing export path: set it in the location (nfs://host/export) or the export parameter")
	}

	root := "/"
	if r := config["root"]; r != "" {
		root = cleanAbs(r)
	}

	cfg := &Config{
		Host:   host,
		Export: export,
		Root:   root,
	}

	cfg.UID, err = parseID(config["uid"], 0)
	if err != nil {
		return nil, fmt.Errorf("invalid uid: %w", err)
	}
	cfg.GID, err = parseID(config["gid"], 0)
	if err != nil {
		return nil, fmt.Errorf("invalid gid: %w", err)
	}

	return cfg, nil
}

// Origin returns the host identifier for a Config, used as the snapshot
// origin.
func (c *Config) Origin() string {
	return c.Host
}

// auth builds the AUTH_UNIX credential presented to the server.
func (c *Config) auth() rpc.Auth {
	hostname, _ := os.Hostname()
	return rpc.NewAuthUnix(hostname, c.UID, c.GID).Auth()
}

// Connect dials the MOUNT service, mounts the export and returns a target
// rooted at it. The caller owns the returned Conn and must Close it.
//
// The address handed to the client is the bare host: go-nfs-client appends
// ":111" itself to reach the portmapper, so a host:port here would produce
// host:port:111 and never dial.
func Connect(cfg *Config) (*Conn, error) {
	addr := cfg.Host

	mount, err := nfs.DialMount(addr)
	if err != nil {
		return nil, fmt.Errorf("dial mount %s: %w", addr, err)
	}

	target, err := mount.Mount(cfg.Export, cfg.auth())
	if err != nil {
		_ = mount.Close()
		return nil, fmt.Errorf("mount export %q on %s: %w", cfg.Export, addr, err)
	}

	return &Conn{
		mount:  mount,
		target: target,
		auth:   cfg.auth(),
		Export: cfg.Export,
	}, nil
}

// Close unmounts the export and tears down the connection.
func (c *Conn) Close() (first error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer recoverErr(&first)
	if c.target != nil {
		if err := c.target.Close(); err != nil {
			first = err
		}
	}
	if c.mount != nil {
		_ = c.mount.Unmount()
		if err := c.mount.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// cleanAbs normalizes p to a cleaned absolute path ("/" for empty input). All
// paths handed to the NFS target are server-side absolute paths within the
// mounted export.
func cleanAbs(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func parseID(s string, def uint32) (uint32, error) {
	if s == "" {
		return def, nil
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}
