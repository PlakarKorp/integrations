// Package rsync is a minimal rsync daemon client speaking protocol 27. It
// lists a module and streams single files, without writing to local disk.
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

// Client talks to one module of an rsync daemon. Each List or Open uses its
// own connection: the daemon serves a single transfer per connection.
type Client struct {
	Addr     string
	Module   string
	User     string
	Password string

	slots chan struct{}
}

// NewClient returns a client opening at most maxConns concurrent connections.
func NewClient(addr, module, user, password string, maxConns int) *Client {
	return &Client{
		Addr:     addr,
		Module:   module,
		User:     user,
		Password: password,
		slots:    make(chan struct{}, maxConns),
	}
}

// List returns the entries below dir, a path relative to the module root,
// recursively, dir itself being named ".". It also returns the errors the
// server reported for paths it could not list.
func (c *Client) List(ctx context.Context, dir string) ([]File, []string, error) {
	s, files, err := c.start(ctx, true, path.Join(dir, ".")+"/")
	if err != nil {
		return nil, nil, err
	}
	defer s.close()
	if err := s.writeDone(); err != nil {
		return nil, nil, err
	}
	if err := s.finish(); err != nil {
		return nil, nil, err
	}
	return files, s.mux.Errors, nil
}

// Open streams the content of the regular file name, a path relative to the
// module root. The returned reader fails if the content does not match the
// checksum sent by the server.
func (c *Client) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if strings.ContainsAny(name, "\n") {
		return nil, fmt.Errorf("%s: newline in file name", name)
	}
	s, files, err := c.start(ctx, false, escapeGlob(name))
	if err != nil {
		return nil, err
	}
	if len(files) != 1 || files[0].Name != path.Base(name) || !files[0].Mode.IsRegular() {
		s.close()
		if len(files) == 0 {
			return nil, fmt.Errorf("%s: %w: %s", name, fs.ErrNotExist, strings.Join(s.mux.Errors, "; "))
		}
		return nil, fmt.Errorf("%s: not a regular file on the server", name)
	}

	// Request the whole file: index 0 with an empty checksum header, so the
	// server sends literal data only.
	if err := s.writeInt32(0, 0, 0, 0, 0); err != nil {
		s.close()
		return nil, err
	}
	if err := s.writeDone(); err != nil {
		s.close()
		return nil, err
	}
	idx, err := s.readInt32()
	if err == nil && idx != 0 {
		err = fmt.Errorf("protocol error: got file index %d", idx)
	}
	if err == nil {
		for range 4 { // checksum header echoed back
			if _, err = s.readInt32(); err != nil {
				break
			}
		}
	}
	if err != nil {
		s.close()
		return nil, err
	}

	h := md4.New()
	h.Write(binary.LittleEndian.AppendUint32(nil, uint32(s.seed)))
	return &fileReader{s: s, name: name, sum: h}, nil
}

type session struct {
	*wire
	conn net.Conn
	mux  *muxReader
	seed int32
	stop func() bool
	done func()
}

func (c *Client) start(ctx context.Context, recursive bool, remotePath string) (*session, []File, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	release := func() { <-c.slots }

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		release()
		return nil, nil, err
	}
	s := &session{
		conn: conn,
		stop: context.AfterFunc(ctx, func() { _ = conn.Close() }),
		done: release,
	}

	rd := bufio.NewReader(conn)
	opts := "-logtp"
	if recursive {
		opts += "r"
	}
	if err := c.handshake(rd, conn, opts, remotePath); err != nil {
		s.close()
		return nil, nil, err
	}

	// The seed is sent before the server switches to multiplexed output.
	raw := &wire{r: rd, w: conn}
	if s.seed, err = raw.readInt32(); err != nil {
		s.close()
		return nil, nil, err
	}
	s.mux = &muxReader{r: rd}
	s.wire = &wire{r: bufio.NewReader(s.mux), w: conn}

	// Empty exclusion list.
	if err := s.writeInt32(0); err != nil {
		s.close()
		return nil, nil, err
	}
	files, err := receiveFileList(s.wire)
	if err != nil {
		s.close()
		return nil, nil, err
	}
	return s, files, nil
}

// rsync/clientserver.c:start_inband_exchange
func (c *Client) handshake(rd *bufio.Reader, w io.Writer, opts, remotePath string) error {
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

	if _, err := fmt.Fprintf(w, "%s\n", c.Module); err != nil {
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
			args := []string{"--server", "--sender", opts, ".", c.Module + "/" + remotePath, ""}
			_, err := io.WriteString(w, strings.Join(args, "\n")+"\n")
			return err
		case strings.HasPrefix(line, "@RSYNCD: AUTHREQD "):
			if c.User == "" {
				return fmt.Errorf("module %s requires authentication", c.Module)
			}
			challenge := strings.TrimPrefix(line, "@RSYNCD: AUTHREQD ")
			if _, err := fmt.Fprintf(w, "%s %s\n", c.User, authResponse(c.Password, challenge)); err != nil {
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
	s.done()
}

type fileReader struct {
	s      *session
	name   string
	sum    hash.Hash
	buf    []byte
	err    error
	closed bool
}

func (r *fileReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		r.err = r.next()
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// next reads one token (rsync/token.c:simple_recv_token): a positive length
// followed by literal data, or 0 at the end of the file.
func (r *fileReader) next() error {
	token, err := r.s.readInt32()
	if err != nil {
		return err
	}
	if token < 0 {
		return fmt.Errorf("%s: protocol error: unexpected block reference", r.name)
	}
	if token > 0 {
		data, err := r.s.readString(int(token))
		if err != nil {
			return err
		}
		r.buf = []byte(data)
		r.sum.Write(r.buf)
		return nil
	}

	remote, err := r.s.readString(md4.Size)
	if err != nil {
		return err
	}
	if !bytes.Equal([]byte(remote), r.sum.Sum(nil)) {
		return fmt.Errorf("%s: checksum mismatch", r.name)
	}
	if err := r.s.finish(); err != nil {
		return err
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

// escapeGlob protects wildcards from the daemon's glob expansion of paths.
func escapeGlob(name string) string {
	var b strings.Builder
	for _, r := range name {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
