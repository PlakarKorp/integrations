package common

import (
	"crypto/tls"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const MB = 1048576

// dialTimeout bounds the TCP/TLS connection setup.
const dialTimeout = 30 * time.Second

// defaultIOTimeout is the idle deadline applied to each socket read/write. A
// server that stalls mid-response (e.g. Gmail under throttling) would otherwise
// block a connection — and, once the pool is exhausted, the whole backup —
// forever. The deadline turns such a stall into a connection error that the
// pool treats as poisoned and recovers from.
const defaultIOTimeout = 2 * time.Minute

var ErrNotExist = fs.ErrNotExist

type ImapConnector struct {
	Address     string
	Username    string
	Password    string
	TlsMode     string
	TlsNoVerify bool
	// IOTimeout overrides the per-operation idle deadline (0 = default).
	IOTimeout time.Duration
}

// idleConn wraps a net.Conn and refreshes a read/write idle deadline on every
// I/O operation, so a connection that goes silent eventually fails instead of
// blocking forever.
type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(b []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Read(b)
}

func (c *idleConn) Write(b []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Write(b)
}

type ImapSession struct {
	Client         *imapclient.Client
	CurrentMailbox string
	buf            []byte

	// rawConn is the underlying socket. Closing it directly breaks a stalled
	// connection without waiting on go-imap's graceful Close (which itself can
	// block on the reader goroutine when the server has stalled mid-transfer).
	rawConn net.Conn
}

// ForceClose closes the underlying socket immediately, unblocking any in-flight
// IMAP operation. Safe to call concurrently with a blocked operation.
func (s *ImapSession) ForceClose() {
	if s != nil && s.rawConn != nil {
		_ = s.rawConn.Close()
	}
}

func (ic *ImapConnector) InitFromConfig(config map[string]string) error {
	location := config["location"]

	endpoint, err := url.Parse(location)
	if err != nil {
		return err
	}

	location = endpoint.Host
	ic.Address = location

	if endpoint.User != nil {
		if endpoint.User.Username() != "" {
			ic.Username = endpoint.User.Username()
		}
		if p, ok := endpoint.User.Password(); ok {
			ic.Password = p
		}
	}

	if ic.Username == "" {
		v, ok := config["username"]
		if !ok {
			return fmt.Errorf("missing username")
		}
		ic.Username = v
	}

	if ic.Password == "" {
		v, ok := config["password"]
		if !ok {
			return fmt.Errorf("missing password")
		}
		ic.Password = v
	}

	v, ok := config["tls"]
	if !ok {
		v = "starttls"
	}
	ic.TlsMode = v

	if config["tls_no_verify"] == "true" {
		ic.TlsNoVerify = true
	}

	if v, ok := config["io_timeout"]; ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid io_timeout %q: %w", v, err)
		}
		ic.IOTimeout = d
	}

	// Default ports when the location omits one.
	if ic.Address != "" && !strings.Contains(ic.Address, ":") {
		switch ic.TlsMode {
		case "tls":
			ic.Address += ":993"
		default:
			ic.Address += ":143"
		}
	}

	return nil
}

func (imp *ImapConnector) Connect() (*ImapSession, error) {
	switch imp.TlsMode {
	case "no-tls", "starttls", "tls":
	default:
		return nil, fmt.Errorf("invalid tls mode %q", imp.TlsMode)
	}

	timeout := imp.IOTimeout
	if timeout == 0 {
		timeout = defaultIOTimeout
	}

	// Dial the raw TCP connection ourselves so we can wrap it with an idle
	// deadline. The deadline wraps the underlying socket, so it remains in
	// effect even after a STARTTLS upgrade.
	rawConn, err := net.DialTimeout("tcp", imp.Address, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to dial IMAP server: %w", err)
	}
	conn := net.Conn(&idleConn{Conn: rawConn, timeout: timeout})

	tlsCfg := &tls.Config{InsecureSkipVerify: imp.TlsNoVerify}
	if host, _, e := net.SplitHostPort(imp.Address); e == nil {
		tlsCfg.ServerName = host
	}
	opts := &imapclient.Options{TLSConfig: tlsCfg}

	var client *imapclient.Client
	switch imp.TlsMode {
	case "no-tls":
		client = imapclient.New(conn, opts)
	case "starttls":
		client, err = imapclient.NewStartTLS(conn, opts)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to start TLS: %w", err)
		}
	case "tls":
		if tlsCfg.NextProtos == nil {
			tlsCfg.NextProtos = []string{"imap"}
		}
		tlsConn := tls.Client(conn, tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS handshake failed: %w", err)
		}
		client = imapclient.New(tlsConn, opts)
	}

	if err := client.Login(imp.Username, imp.Password).Wait(); err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to login: %w", err)
	}

	return &ImapSession{
		Client:  client,
		buf:     make([]byte, 2*MB),
		rawConn: conn,
	}, nil
}

func (session *ImapSession) Select(mailbox string, readOnly bool) error {
	_, err := session.Client.Select(mailbox, &imap.SelectOptions{
		ReadOnly: readOnly,
	}).Wait()
	if err != nil {
		return fmt.Errorf("SELECT command failed: %w", err)
	}
	return nil
}

func (session *ImapSession) Create(mailbox string, existOk bool) error {
	opts := imap.CreateOptions{}
	err := session.Client.Create(mailbox, &opts).Wait()
	if err != nil {
		if existOk && IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

func (session *ImapSession) List() ([]*imap.ListData, error) {
	// Basic RFC3501 LIST, compatible with most servers
	res, err := session.Client.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("LIST command failed: %w", err)
	}
	return res, nil
}

func (session *ImapSession) Append(mailbox string, fp io.Reader, size int64) error {
	opts := imap.AppendOptions{}
	appendCmd := session.Client.Append(mailbox, size, &opts)
	w, err := io.CopyBuffer(appendCmd, fp, session.buf)
	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}
	if w != size {
		return fmt.Errorf("inconsistent number of bytes written")
	}
	err = appendCmd.Close()
	if err != nil {
		return fmt.Errorf("failed to close message: %w", err)
	}
	_, err = appendCmd.Wait()
	if err != nil {
		return fmt.Errorf("APPEND command failed: %w", err)
	}

	return nil
}

func (session *ImapSession) Logout() error {
	if session == nil || session.Client == nil {
		return nil
	}
	return session.Client.Logout().Wait()
}

// IsAlreadyExists reports whether err is an IMAP error indicating the mailbox
// already exists (so CREATE can be treated as idempotent).
func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*imap.Error); ok && e.Code == imap.ResponseCodeAlreadyExists {
		return true
	}
	up := strings.ToUpper(err.Error())
	return strings.Contains(up, "ALREADYEXISTS") || strings.Contains(up, "ALREADY EXISTS")
}

// IsTryCreate reports whether an APPEND failed because the target mailbox does
// not exist yet (RFC 3501 [TRYCREATE] response code).
func IsTryCreate(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*imap.Error); ok && e.Code == imap.ResponseCodeTryCreate {
		return true
	}
	return strings.Contains(strings.ToUpper(err.Error()), "TRYCREATE")
}

func SafeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "message"
	}

	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_' || r == '.':
			return r
		default:
			return '_'
		}
	}, s)
	s = strings.Join(strings.Fields(s), " ") // collapse spaces
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
