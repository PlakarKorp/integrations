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

// Package common holds the SMB-specific plumbing shared by the importer and
// exporter: parsing the smb:// location, dialing the server, authenticating
// with NTLMv2 and mounting a share.
//
// It speaks SMB2/3 directly over TCP using github.com/hirochachacha/go-smb2;
// there is no kernel mount and no dependency on a system smbclient.
package common

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"
)

const defaultPort = "445"

// fileInfo is the os.FileInfo go-smb2 returns; its concrete type is
// *smb2.FileStat, whose Mode() correctly carries Go's ModeDir/ModeSymlink bits.
type fileInfo = os.FileInfo

// Conn bundles a dialed SMB session and a mounted share. Share exposes an
// os-like VFS rooted at the share; the TCP connection and session are kept so
// they can be torn down on Close.
type Conn struct {
	conn    net.Conn
	session *smb2.Session
	Share   *smb2.Share
}

// Config is the parsed, normalized form of a connector configuration map.
type Config struct {
	Host         string
	Port         string
	Share        string // SMB share (tree) name, e.g. "data"
	Root         string // path to walk within the share, "/" for the whole share
	User         string
	Password     string
	Domain       string
	mountTimeout time.Duration
}

// ParseConfig turns a connector config map into a Config.
//
// The location is an smb:// URL of the form:
//
//	smb://[<user>[:<password>]@]<host>[:<port>]/<share>[/<subpath>]
//
// The first path segment is the SMB share to mount; any deeper subpath (or the
// "root" option) narrows the walk within that share. Credentials may be given
// in the URL userinfo or via the username/password options (but not both).
//
// The share to mount can also be given out-of-band through the "share"
// parameter, which takes precedence over the first path segment. This lets a
// UI expose the share as its own field instead of burying it in the location
// URL; when it is set the location may be a bare smb://<host>[:<port>], and any
// URL subpath still narrows the walk.
func ParseConfig(config map[string]string) (*Config, error) {
	target := config["location"]
	if target == "" {
		return nil, fmt.Errorf("missing location")
	}

	parsed, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("invalid location %q: %w", target, err)
	}
	if parsed.Scheme != "smb" {
		return nil, fmt.Errorf("location scheme must be smb://, got %q", parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing host in location %q", target)
	}

	port := parsed.Port()
	if port == "" {
		port = config["port"]
	}
	if port == "" {
		port = defaultPort
	}

	share, sub := splitShare(parsed.Path)
	// The share field, when set, takes precedence over the URL's first path
	// segment; any URL subpath still narrows the walk.
	if s := config["share"]; s != "" {
		share = strings.Trim(s, "/")
	}
	if share == "" {
		return nil, fmt.Errorf("missing share: set it in the location (smb://host/share) or the share parameter")
	}

	root := sub
	if r := config["root"]; r != "" {
		root = cleanRel(r)
	}
	if root == "" {
		root = "/"
	}

	cfg := &Config{
		Host:         host,
		Port:         port,
		Share:        share,
		Root:         root,
		User:         config["username"],
		Password:     config["password"],
		Domain:       config["domain"],
		mountTimeout: 15 * time.Second,
	}

	// Credentials from the URL userinfo, if present.
	if parsed.User != nil {
		if cfg.User != "" {
			return nil, fmt.Errorf("can not use user@host syntax and username parameter")
		}
		cfg.User = parsed.User.Username()
		if pw, ok := parsed.User.Password(); ok {
			if cfg.Password != "" {
				return nil, fmt.Errorf("can not use user:password@host syntax and password parameter")
			}
			cfg.Password = pw
		}
	}

	if cfg.User == "" {
		// SMB requires a principal; "Guest" is the conventional anonymous user.
		cfg.User = "Guest"
	}

	return cfg, nil
}

// Origin returns the host[:port]/share identifier for a Config.
func (c *Config) Origin() string {
	hostport := c.Host
	if c.Port != "" && c.Port != defaultPort {
		hostport = net.JoinHostPort(c.Host, c.Port)
	}
	return hostport + "/" + c.Share
}

// Connect dials the server, authenticates and mounts the share. The caller owns
// the returned Conn and must Close it.
func Connect(ctx context.Context, cfg *Config) (*Conn, error) {
	addr := net.JoinHostPort(cfg.Host, cfg.Port)

	d := net.Dialer{Timeout: cfg.mountTimeout}
	tcpConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	dialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     cfg.User,
			Password: cfg.Password,
			Domain:   cfg.Domain,
		},
	}

	session, err := dialer.DialContext(ctx, tcpConn)
	if err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("smb session setup on %s: %w", addr, err)
	}

	share, err := session.Mount(cfg.Share)
	if err != nil {
		session.Logoff()
		tcpConn.Close()
		return nil, fmt.Errorf("mount share %q on %s: %w", cfg.Share, addr, err)
	}

	return &Conn{
		conn:    tcpConn,
		session: session,
		Share:   share,
	}, nil
}

// Lstat returns the attributes of p (a canonical "/abs" share path) without
// following a final symlink.
func (c *Conn) Lstat(p string) (fileInfo, error) {
	return c.Share.Lstat(SharePath(p))
}

// ReadDir lists the directory at p.
func (c *Conn) ReadDir(p string) ([]fileInfo, error) {
	return c.Share.ReadDir(SharePath(p))
}

// Readlink returns the target of the symlink at p. SMB stores symlink targets
// with '\' separators (go-smb2 normalizes them on write); we translate back to
// '/' so the target Plakar records is portable to other backends.
func (c *Conn) Readlink(p string) (string, error) {
	target, err := c.Share.Readlink(SharePath(p))
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(target, `\`, "/"), nil
}

// Open opens the file at p for reading. The returned handle is safe to read
// concurrently with other operations on the share.
func (c *Conn) Open(p string) (*smb2.File, error) {
	return c.Share.Open(SharePath(p))
}

// MkdirAll creates the directory at p and any missing parents.
func (c *Conn) MkdirAll(p string, perm os.FileMode) error {
	return c.Share.MkdirAll(SharePath(p), perm)
}

// Create truncates/creates the file at p for writing.
func (c *Conn) Create(p string) (*smb2.File, error) {
	return c.Share.Create(SharePath(p))
}

// Symlink creates a symlink at linkpath pointing at target.
func (c *Conn) Symlink(target, linkpath string) error {
	return c.Share.Symlink(target, SharePath(linkpath))
}

// Chtimes sets the modification time of p.
func (c *Conn) Chtimes(p string, mtime time.Time) error {
	return c.Share.Chtimes(SharePath(p), mtime, mtime)
}

// Close unmounts the share and tears down the session. Logoff closes the
// underlying TCP transport itself, so we only close the raw connection as a
// fallback when there is no session to log off (and ignore the resulting
// "already closed" error in the normal path).
func (c *Conn) Close() error {
	var first error
	if c.Share != nil {
		if err := c.Share.Umount(); err != nil {
			first = err
		}
	}
	if c.session != nil {
		if err := c.session.Logoff(); err != nil && first == nil {
			first = err
		}
		return first // Logoff already closed the transport
	}
	if c.conn != nil {
		if err := c.conn.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// splitShare splits a URL path "/share/sub/dir" into the share name ("share")
// and the cleaned subpath within it ("/sub/dir", or "/" if none).
func splitShare(p string) (share, sub string) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" || p == "." {
		return "", "/"
	}
	parts := strings.SplitN(p, "/", 2)
	share = parts[0]
	if len(parts) == 2 && parts[1] != "" {
		sub = "/" + parts[1]
	} else {
		sub = "/"
	}
	return share, sub
}

// cleanRel normalizes a user-supplied root to a cleaned absolute-looking path
// within the share ("/" for empty input).
func cleanRel(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}
