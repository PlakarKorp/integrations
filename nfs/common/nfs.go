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
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/vmware/go-nfs-client/nfs"
	"github.com/vmware/go-nfs-client/nfs/rpc"
)

// Conn bundles a mounted NFS export and serializes access to it.
//
// The underlying go-nfs-client transport is NOT safe for concurrent RPCs: a
// request write and its reply read are locked separately, so interleaved calls
// trip the XID check and corrupt the stream. A single NFS file transfer is many
// RPCs, so the lock is held for the whole of an operation (an entire file read
// or write), not per-RPC. Every NFS access goes through the guarded methods on
// Conn; callers must not touch the raw target.
type Conn struct {
	mu     sync.Mutex
	mount  *nfs.Mount
	target *nfs.Target

	// Export is the server path that was mounted (the export point).
	Export string
}

// Lookup resolves p relative to the export, returning its attributes (nil for
// the export root, which has no final component) and filehandle.
func (c *Conn) Lookup(p string) (*nfs.Fattr, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, fh, err := c.target.Lookup(p)
	if err != nil {
		return nil, nil, err
	}
	attr, _ := info.(*nfs.Fattr)
	return attr, fh, nil
}

// ReadDirPlus lists dir with attributes in a single round-trip.
func (c *Conn) ReadDirPlus(dir string) ([]*nfs.EntryPlus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target.ReadDirPlus(dir)
}

// Readlink returns the target of the symlink at p.
func (c *Conn) Readlink(p string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := c.target.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return f.Readlink()
}

// Mkdir creates a directory at p with the given permissions.
func (c *Conn) Mkdir(p string, perm os.FileMode) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.target.Mkdir(p, perm)
	return err
}

// OpenReader opens the file at p for streaming reads. It acquires the
// connection lock and holds it until the returned ReadCloser is closed, so the
// caller MUST close it promptly: while it is open no other NFS operation on
// this connection can proceed. This serialization is required by the
// non-multiplexing transport.
func (c *Conn) OpenReader(p string) (io.ReadCloser, error) {
	c.mu.Lock()
	f, err := c.target.Open(p)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	return &lockedFile{conn: c, file: f}, nil
}

// lockedFile is an io.ReadCloser over an NFS file that releases the connection
// lock exactly once, on Close.
type lockedFile struct {
	conn   *Conn
	file   *nfs.File
	closed bool
}

func (lf *lockedFile) Read(p []byte) (int, error) {
	return lf.file.Read(p)
}

func (lf *lockedFile) Close() error {
	if lf.closed {
		return nil
	}
	lf.closed = true
	err := lf.file.Close()
	lf.conn.mu.Unlock()
	return err
}

// WriteFile creates the file at p with the given permissions and streams r into
// it. The connection lock is held for the entire transfer.
func (c *Conn) WriteFile(p string, perm os.FileMode, r io.Reader) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := c.target.OpenFile(p, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Config is the parsed, normalized form of a connector configuration map.
type Config struct {
	// Host is the NFS server hostname or IP.
	Host string
	// Port is the MOUNT/NFS service port. NFS servers are normally reached
	// through the portmapper, so this is only set when the user pins it.
	Port string
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
//	nfs://<host>[:<port>]/<export>[/<subpath>]
//
// Everything in the URL path is treated as the export to mount; a "root"
// parameter (or a deeper subpath) narrows the walk within that export. This
// matches mount.nfs semantics, where the server hands out a filehandle for a
// path it explicitly exports.
//
// The export to mount can also be given out-of-band through the "export"
// parameter, which takes precedence over the URL path. This lets a UI expose
// the mountpoint as its own field instead of burying it in the location URL;
// when it is set the location may be a bare nfs://<host>[:<port>].
func ParseConfig(config map[string]string) (*Config, error) {
	target := config["location"]
	if target == "" {
		return nil, fmt.Errorf("missing location")
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

	port := parsed.Port()
	if port == "" {
		port = config["port"]
	}

	// The export field, when set, takes precedence over the URL path.
	export := cleanAbs(parsed.Path)
	if e := config["export"]; e != "" {
		export = cleanAbs(e)
	}
	if export == "/" || export == "" {
		return nil, fmt.Errorf("missing export path: set it in the location (nfs://host/export) or the export parameter")
	}

	root := "/"
	if r := config["root"]; r != "" {
		root = cleanAbs(r)
	}

	cfg := &Config{
		Host:   host,
		Port:   port,
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

// Origin returns the host[:port] identifier for a Config, used as the snapshot
// origin.
func (c *Config) Origin() string {
	if c.Port != "" {
		return net.JoinHostPort(c.Host, c.Port)
	}
	return c.Host
}

// auth builds the AUTH_UNIX credential presented to the server.
func (c *Config) auth() rpc.Auth {
	hostname, _ := os.Hostname()
	return rpc.NewAuthUnix(hostname, c.UID, c.GID).Auth()
}

// Connect dials the MOUNT service, mounts the export and returns a target
// rooted at it. The caller owns the returned Conn and must Close it.
func Connect(cfg *Config) (*Conn, error) {
	addr := cfg.Host
	if cfg.Port != "" {
		addr = net.JoinHostPort(cfg.Host, cfg.Port)
	}

	mount, err := nfs.DialMount(addr)
	if err != nil {
		return nil, fmt.Errorf("dial mount %s: %w", addr, err)
	}

	target, err := mount.Mount(cfg.Export, cfg.auth())
	if err != nil {
		mount.Close()
		return nil, fmt.Errorf("mount export %q on %s: %w", cfg.Export, addr, err)
	}

	return &Conn{
		mount:  mount,
		target: target,
		Export: cfg.Export,
	}, nil
}

// Close unmounts the export and tears down the connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	if c.target != nil {
		if err := c.target.Close(); err != nil {
			first = err
		}
	}
	if c.mount != nil {
		c.mount.Unmount()
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
