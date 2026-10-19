// Package rsync is a minimal rsync daemon client speaking protocol 27. It
// lists a module, streams single files from it and sends files to it, without
// touching the local disk.
//
// The protocol handling is adapted from github.com/gokrazy/rsync, whose
// client only writes into a local directory. See LICENSE in this directory.
package rsync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net"
	"path"
	"strings"

	"golang.org/x/crypto/md4" //nolint:staticcheck // the protocol mandates MD4
)

const protocolVersion = 27

// Options the server runs with, as rsync command-line flags. When the server
// sends:
//
//	-l  send symlinks as symlinks, with their target
//	-o  send each file's uid, and the user names after the list
//	-g  send each file's gid, and the group names after the list
//	-t  send each file's modification time
//	-p  send each file's permissions
//	-r  recurse into directories
//	-d  send a directory itself without recursing; without -r or -d, the
//	    server skips directories
//
// When the server receives, -l, -o, -g, -t, -p and -d apply what the file list
// holds, and:
//
//	-I  rewrite every file, even one whose size and time match
//
// -l, -o, -g, -t and -p set the file list layout, both ways.
const (
	listOpts = "-logtpr"
	pingOpts = "-logtpd"
	openOpts = "-logtp"
	sendOpts = "-logtpdI"
)

// Each List or Open uses its own connection: the daemon serves a single
// transfer per connection.
type Client struct {
	addr     string
	module   string
	user     string
	password string
	slots    chan struct{}
}

func NewClient(addr, module, user, password string, maxConns int) *Client {
	return &Client{
		addr:     addr,
		module:   module,
		user:     user,
		password: password,
		slots:    make(chan struct{}, maxConns),
	}
}

// List returns name, a path relative to the module root, and the entries
// below it if it is a directory. name itself is named "." and the entries are
// relative to it. List also returns the errors the server reported for paths
// it could not list.
func (c *Client) List(ctx context.Context, name string) ([]File, []string, error) {
	return c.list(ctx, name, listOpts)
}

func (c *Client) Ping(ctx context.Context, name string) error {
	_, _, err := c.list(ctx, name, pingOpts)
	return err
}

func (c *Client) list(ctx context.Context, name, opts string) (files []File, errs []string, err error) {
	// Without a trailing slash, the server names the entries after the last
	// component of name, so that name may also be a file.
	name = path.Join(name, ".")
	s, files, err := c.start(ctx, opts, name)
	if err != nil {
		return nil, nil, err
	}
	defer s.close()
	// The server exits on an empty list.
	if len(files) == 0 {
		return nil, nil, s.wrap(fmt.Errorf("%s: %w", name, fs.ErrNotExist))
	}
	if err := s.writeDone(); err != nil {
		return nil, nil, s.wrap(err)
	}
	if err := s.finish(); err != nil {
		return nil, nil, s.wrap(err)
	}

	if base := path.Base(name); base != "." {
		for i, f := range files {
			switch rel, ok := strings.CutPrefix(f.Name, base+"/"); {
			case f.Name == base:
				files[i].Name = "."
			case ok:
				files[i].Name = rel
			default:
				return nil, nil, fmt.Errorf("server sent file name %q outside of %q", f.Name, name)
			}
		}
	}
	return files, s.mux.errors, nil
}

// The reader returned by Open fails if the content does not match the
// checksum sent by the server.
func (c *Client) Open(ctx context.Context, name string) (_ io.ReadCloser, err error) {
	s, files, err := c.start(ctx, openOpts, name)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = s.wrap(err)
			s.close()
		}
	}()

	if len(files) == 0 {
		return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
	}
	if len(files) != 1 || files[0].Name != path.Base(name) || !files[0].Mode.IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file on the server", name)
	}

	// Request the whole file: index 0 with an empty checksum header, so the
	// server sends literal data only.
	if err := s.writeInt32(0, 0, 0, 0, 0); err != nil {
		return nil, err
	}
	if err := s.writeDone(); err != nil {
		return nil, err
	}
	idx, err := s.readInt32()
	if err != nil {
		return nil, err
	}
	if idx != 0 {
		return nil, fmt.Errorf("protocol error: got file index %d", idx)
	}
	for range 4 { // checksum header echoed back
		if _, err := s.readInt32(); err != nil {
			return nil, err
		}
	}

	h := md4.New()
	h.Write(binary.LittleEndian.AppendUint32(nil, uint32(s.seed)))
	return &fileReader{s: s, name: name, sum: h}, nil
}

type session struct {
	*wire
	conn  net.Conn
	mux   *muxReader
	seed  int32
	stop  func() bool
	slots chan struct{}
}

// start requests remotePath from the server and receives its file list.
func (c *Client) start(ctx context.Context, opts, remotePath string) (_ *session, _ []File, err error) {
	s, err := c.connect(ctx, true, opts, escapeGlob(remotePath))
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			err = s.wrap(err)
			s.close()
		}
	}()

	// Empty exclusion list.
	if err := s.writeInt32(0); err != nil {
		return nil, nil, err
	}
	files, err := receiveFileList(s.wire)
	if err != nil {
		return nil, nil, err
	}
	return s, files, nil
}

// connect opens a connection and starts the server with opts on remotePath,
// as the sender or as the receiver.
func (c *Client) connect(ctx context.Context, sender bool, opts, remotePath string) (_ *session, err error) {
	// Arguments are newline-separated in the protocol.
	if strings.ContainsRune(remotePath, '\n') {
		return nil, fmt.Errorf("%q: newline in path", remotePath)
	}

	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		<-c.slots
		return nil, err
	}
	s := &session{
		conn:  conn,
		stop:  context.AfterFunc(ctx, func() { _ = conn.Close() }),
		slots: c.slots,
		mux:   &muxReader{},
	}
	defer func() {
		if err != nil {
			err = s.wrap(err)
			s.close()
		}
	}()

	rd := bufio.NewReader(conn)
	if err := c.handshake(rd, conn, sender, opts, remotePath); err != nil {
		return nil, err
	}

	// The seed is sent before the server switches to multiplexed output.
	raw := &wire{r: rd, w: conn}
	if s.seed, err = raw.readInt32(); err != nil {
		return nil, err
	}
	s.mux.r = rd
	s.wire = &wire{r: s.mux, w: conn}
	return s, nil
}

// rsync/clientserver.c:start_inband_exchange
func (c *Client) handshake(rd *bufio.Reader, w io.Writer, sender bool, opts, remotePath string) error {
	if _, err := fmt.Fprintf(w, "@RSYNCD: %d\n", protocolVersion); err != nil {
		return err
	}
	greeting, err := rd.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading server greeting: %w", err)
	}
	var remote int
	if _, err := fmt.Sscanf(greeting, "@RSYNCD: %d", &remote); err != nil {
		return fmt.Errorf("invalid server greeting %q", strings.TrimSpace(greeting))
	}
	if remote < protocolVersion {
		return fmt.Errorf("server protocol version %d is older than %d", remote, protocolVersion)
	}

	if _, err := fmt.Fprintf(w, "%s\n", c.module); err != nil {
		return err
	}
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading server response: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "@RSYNCD: OK":
			args := []string{"--server"}
			if sender {
				args = append(args, "--sender")
			}
			args = append(args, opts, ".", c.module+"/"+remotePath, "")
			_, err := io.WriteString(w, strings.Join(args, "\n")+"\n")
			return err
		case strings.HasPrefix(line, "@RSYNCD: AUTHREQD "):
			if c.user == "" {
				return fmt.Errorf("module %s requires authentication", c.module)
			}
			challenge := strings.TrimPrefix(line, "@RSYNCD: AUTHREQD ")
			if _, err := fmt.Fprintf(w, "%s %s\n", c.user, authResponse(c.password, challenge)); err != nil {
				return err
			}
		case line == "@RSYNCD: EXIT":
			return fmt.Errorf("server closed the session")
		case strings.HasPrefix(line, "@ERROR"):
			return errors.New(strings.TrimSpace(strings.TrimPrefix(line, "@ERROR:")))
		}
		// Anything else is the message of the day.
	}
}

// authResponse is rsync/authenticate.c:generate_hash for protocol < 30: MD4
// over a zero seed, the password and the challenge, base64 without padding.
func authResponse(password, challenge string) string {
	h := md4.New()
	h.Write([]byte{0, 0, 0, 0})
	h.Write([]byte(password))
	h.Write([]byte(challenge))
	return base64.RawStdEncoding.EncodeToString(h.Sum(nil))
}

// writeDone ends both phases of the generator: no more files are requested.
func (s *session) writeDone() error {
	return s.writeInt32(-1, -1)
}

// finish reads the end of both phases and the statistics, then sends the
// final goodbye (rsync/main.c:do_recv).
func (s *session) finish() error {
	for range 2 {
		v, err := s.readInt32()
		if err != nil {
			return err
		}
		if v != -1 {
			return fmt.Errorf("protocol error: expected end of phase, got %d", v)
		}
	}
	for range 3 {
		if _, err := s.readInt64(); err != nil {
			return err
		}
	}
	return s.writeInt32(-1)
}

func (s *session) close() {
	s.stop()
	_ = s.conn.Close()
	<-s.slots
}

// wrap adds the errors the server reported to err: they usually say why the
// transfer failed.
func (s *session) wrap(err error) error {
	if len(s.mux.errors) == 0 {
		return err
	}
	return fmt.Errorf("%w (server: %s)", err, strings.Join(s.mux.errors, "; "))
}

type fileReader struct {
	s      *session
	name   string
	sum    hash.Hash
	left   int // bytes left in the current data token
	err    error
	closed bool
}

func (r *fileReader) Read(p []byte) (int, error) {
	for r.left == 0 {
		if r.err != nil {
			return 0, r.err
		}
		r.err = r.next()
	}
	n, err := r.s.r.Read(p[:min(len(p), r.left)])
	r.sum.Write(p[:n])
	r.left -= n
	if err != nil {
		r.err = r.fail(err)
	}
	return n, r.err
}

// fail reports an io.EOF as unexpected: only next returns io.EOF, once the
// checksum matched.
func (r *fileReader) fail(err error) error {
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return r.s.wrap(err)
}

// next reads one token header (rsync/token.c:simple_recv_token): a positive
// length followed by literal data, or 0 at the end of the file.
func (r *fileReader) next() error {
	token, err := r.s.readInt32()
	if err != nil {
		return r.fail(err)
	}
	if token < 0 {
		return r.fail(fmt.Errorf("%s: protocol error: unexpected block reference", r.name))
	}
	if token > 0 {
		r.left = int(token)
		return nil
	}

	var remote [md4.Size]byte
	if _, err := io.ReadFull(r.s.r, remote[:]); err != nil {
		return r.fail(err)
	}
	if !bytes.Equal(remote[:], r.sum.Sum(nil)) {
		return r.fail(fmt.Errorf("%s: checksum mismatch", r.name))
	}
	if err := r.s.finish(); err != nil {
		return r.fail(err)
	}
	return io.EOF
}

func (r *fileReader) Close() error {
	if !r.closed {
		r.closed = true
		r.s.close()
	}
	return nil
}

// escapeGlob protects wildcards from the daemon's glob expansion of paths, the
// destination included.
// The daemon matches components holding a wildcard with wildmatch, which
// honours backslash escapes, and looks the others up literally.
func escapeGlob(name string) string {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if strings.ContainsAny(part, "*?[") {
			parts[i] = globEscaper.Replace(part)
		}
	}
	return strings.Join(parts, "/")
}

var globEscaper = strings.NewReplacer(`*`, `\*`, `?`, `\?`, `[`, `\[`, `\`, `\\`)
