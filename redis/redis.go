package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

const dumpRecordPath = "/dump.rdb"

func init() {
	importer.Register("redis", location.FLAG_STREAM, NewImporter)
	importer.Register("rediss", location.FLAG_STREAM, NewImporter)
	exporter.Register("redis+file", location.FLAG_LOCALFS, NewExporter)
}

type config struct {
	address            string
	host               string
	username           string
	password           string
	tls                bool
	tlsServerName      string
	insecureSkipVerify bool
	timeout            time.Duration
	outputPath         string
}

type connector struct {
	cfg   config
	proto string
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, raw map[string]string) (importer.Importer, error) {
	cfg, err := parseRedisConfig(proto, raw)
	if err != nil {
		return nil, err
	}
	return &connector{cfg: cfg, proto: proto}, nil
}

func NewExporter(ctx context.Context, opts *connectors.Options, proto string, raw map[string]string) (exporter.Exporter, error) {
	cfg, err := parseFileConfig(proto, raw)
	if err != nil {
		return nil, err
	}
	return &connector{cfg: cfg, proto: proto}, nil
}

func parseRedisConfig(proto string, raw map[string]string) (config, error) {
	cfg := config{host: "localhost", timeout: 30 * time.Second}
	loc := raw["location"]
	if loc == "" {
		loc = proto + "://localhost:6379"
	}

	u, err := url.Parse(loc)
	if err != nil {
		return cfg, fmt.Errorf("parse location: %w", err)
	}
	if u.Scheme != "" && u.Scheme != "redis" && u.Scheme != "rediss" {
		return cfg, fmt.Errorf("unsupported Redis scheme %q", u.Scheme)
	}
	if u.Scheme == "rediss" || proto == "rediss" {
		cfg.tls = true
	}
	if u.Hostname() != "" {
		cfg.host = u.Hostname()
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	if u.User != nil {
		cfg.username = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			cfg.password = pw
		} else if cfg.username != "" {
			cfg.password = cfg.username
			cfg.username = ""
		}
	}

	if v := raw["host"]; v != "" {
		cfg.host = v
	}
	if v := raw["port"]; v != "" {
		port = v
	}
	if v := raw["username"]; v != "" {
		cfg.username = v
	}
	if v := raw["password"]; v != "" {
		cfg.password = v
	}
	if v := raw["tls"]; v != "" {
		cfg.tls, err = strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("tls: %w", err)
		}
	}
	if v := raw["tls_server_name"]; v != "" {
		cfg.tlsServerName = v
	}
	if v := raw["insecure_skip_verify"]; v != "" {
		cfg.insecureSkipVerify, err = strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("insecure_skip_verify: %w", err)
		}
	}
	if v := raw["timeout"]; v != "" {
		cfg.timeout, err = parseTimeout(v)
		if err != nil {
			return cfg, err
		}
	}
	if cfg.tlsServerName == "" {
		cfg.tlsServerName = cfg.host
	}
	if cfg.host == "" {
		return cfg, fmt.Errorf("missing Redis host")
	}
	cfg.address = net.JoinHostPort(cfg.host, port)
	return cfg, nil
}

func parseFileConfig(proto string, raw map[string]string) (config, error) {
	cfg := config{}
	if v := raw["path"]; v != "" {
		cfg.outputPath = v
	} else if v := raw["output"]; v != "" {
		cfg.outputPath = v
	} else if loc := raw["location"]; loc != "" {
		u, err := url.Parse(loc)
		if err != nil {
			return cfg, fmt.Errorf("parse location: %w", err)
		}
		if u.Scheme != "" && u.Scheme != "redis+file" {
			return cfg, fmt.Errorf("unsupported Redis file scheme %q", u.Scheme)
		}
		cfg.outputPath = fileURLPath(u)
	}
	if cfg.outputPath == "" {
		return cfg, fmt.Errorf("missing output path")
	}
	return cfg, nil
}

func fileURLPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.Opaque != "" {
		return u.Opaque
	}
	p, _ := url.PathUnescape(u.Path)
	p = filepath.FromSlash(p)
	if u.Host != "" && p != "" {
		return string(os.PathSeparator) + filepath.Join(u.Host, p)
	}
	if u.Host != "" {
		return u.Host
	}
	return p
}

func parseTimeout(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	seconds, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("timeout: %w", err)
	}
	return time.Duration(seconds) * time.Second, nil
}

func (c *connector) Root() string { return "/" }
func (c *connector) Type() string { return "redis" }

func (c *connector) Origin() string {
	if c.cfg.address != "" {
		return c.cfg.address
	}
	return c.cfg.outputPath
}

func (c *connector) Flags() location.Flags {
	if c.proto == "redis+file" {
		return location.FLAG_LOCALFS
	}
	return location.FLAG_STREAM
}

func (c *connector) Ping(ctx context.Context) error {
	if c.proto == "redis+file" {
		return nil
	}
	conn, br, err := c.openConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := c.authenticate(conn, br); err != nil {
		return err
	}
	_, err = doCommand(conn, br, "PING")
	return err
}

func (c *connector) Close(ctx context.Context) error { return nil }

func (c *connector) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	finfo := objects.FileInfo{
		Lname:    path.Base(dumpRecordPath),
		Lsize:    -1,
		Lmode:    0o444,
		LmodTime: time.Now(),
	}

	records <- connectors.NewRecord(dumpRecordPath, "", finfo, nil, func() (io.ReadCloser, error) {
		return c.openRDB(ctx)
	})
	return nil
}

func (c *connector) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case record, ok := <-records:
			if !ok {
				return nil
			}
			if record.Err != nil {
				results <- record.Ok()
				continue
			}
			if record.IsXattr {
				results <- record.Error(fmt.Errorf("unexpected xattr %q", record.Pathname))
				continue
			}
			if record.FileInfo.Lmode.IsDir() {
				results <- record.Ok()
				continue
			}
			if err := c.restoreRecord(ctx, record); err != nil {
				results <- record.Error(err)
			} else {
				results <- record.Ok()
			}
		}
	}
}

func (c *connector) restoreRecord(ctx context.Context, record *connectors.Record) error {
	if record.Reader == nil {
		return fmt.Errorf("record %q has no reader", record.Pathname)
	}
	if filepath.Base(record.Pathname) != "dump.rdb" && filepath.Ext(record.Pathname) != ".rdb" {
		return fmt.Errorf("unexpected Redis dump record %q", record.Pathname)
	}

	target := targetPath(c.cfg.outputPath, record.Pathname)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	_, copyErr := copyWithContext(ctx, tmp, record.Reader)
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

func targetPath(base, recordPath string) string {
	if base == "" {
		base = "dump.rdb"
	}
	if info, err := os.Stat(base); err == nil && info.IsDir() {
		return filepath.Join(base, filepath.Base(recordPath))
	}
	if strings.HasSuffix(base, string(os.PathSeparator)) || strings.HasSuffix(base, "/") {
		return filepath.Join(base, filepath.Base(recordPath))
	}
	return base
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			written += int64(nw)
			if ew != nil {
				return written, ew
			}
			if nw != nr {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if errors.Is(er, io.EOF) {
				return written, nil
			}
			return written, er
		}
	}
}

func (c *connector) openConn(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	dialer := net.Dialer{Timeout: c.cfg.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.cfg.address)
	if err != nil {
		return nil, nil, err
	}
	if c.cfg.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(c.cfg.timeout))
	}
	if c.cfg.tls {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         c.cfg.tlsServerName,
			InsecureSkipVerify: c.cfg.insecureSkipVerify,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
		conn = tlsConn
	}
	return conn, bufio.NewReader(conn), nil
}

func (c *connector) authenticate(conn net.Conn, br *bufio.Reader) error {
	if c.cfg.password == "" {
		return nil
	}
	if c.cfg.username != "" {
		_, err := doCommand(conn, br, "AUTH", c.cfg.username, c.cfg.password)
		return err
	}
	_, err := doCommand(conn, br, "AUTH", c.cfg.password)
	return err
}

func (c *connector) openRDB(ctx context.Context) (io.ReadCloser, error) {
	r, err := c.openRDBWithCommand(ctx, "PSYNC", "?", "-1")
	if err == nil {
		return r, nil
	}
	if !canFallbackToSync(err) {
		return nil, err
	}
	return c.openRDBWithCommand(ctx, "SYNC")
}

func canFallbackToSync(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToUpper(err.Error())
	return strings.Contains(s, "PSYNC") || strings.Contains(s, "UNKNOWN COMMAND") || strings.Contains(s, "NOPSYNC")
}

func (c *connector) openRDBWithCommand(ctx context.Context, args ...string) (io.ReadCloser, error) {
	conn, br, err := c.openConn(ctx)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()

	if err := c.authenticate(conn, br); err != nil {
		return nil, err
	}
	if err := sendCommand(conn, args...); err != nil {
		return nil, err
	}

	line, err := readLine(br)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(line, "-") {
		return nil, fmt.Errorf("redis %s: %s", args[0], strings.TrimPrefix(line, "-"))
	}
	if strings.HasPrefix(line, "+") {
		line, err = readLine(br)
		if err != nil {
			return nil, err
		}
	}
	if !strings.HasPrefix(line, "$") {
		return nil, fmt.Errorf("unexpected Redis RDB response %q", line)
	}
	if strings.HasPrefix(line, "$EOF:") {
		return nil, fmt.Errorf("Redis returned an EOF-delimited RDB stream, which is not supported without REPLCONF capa eof")
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(line, "$"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse RDB length: %w", err)
	}
	if n < 0 {
		return nil, fmt.Errorf("Redis returned a nil RDB stream")
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return &rdbReader{conn: conn, reader: &io.LimitedReader{R: br, N: n}}, nil
}

type rdbReader struct {
	conn   net.Conn
	reader *io.LimitedReader
}

func (r *rdbReader) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *rdbReader) Close() error {
	return r.conn.Close()
}

func sendCommand(w io.Writer, args ...string) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

func doCommand(w io.Writer, br *bufio.Reader, args ...string) (string, error) {
	if err := sendCommand(w, args...); err != nil {
		return "", err
	}
	line, err := readLine(br)
	if err != nil {
		return "", err
	}
	if line == "" {
		return "", fmt.Errorf("empty Redis response")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return "", fmt.Errorf("redis: %s", line[1:])
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return "", err
		}
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	default:
		return "", fmt.Errorf("unexpected Redis response %q", line)
	}
}

func readLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}
