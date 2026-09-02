package redis

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseRedisConfigFromURI(t *testing.T) {
	cfg, err := parseRedisConfig("redis", map[string]string{
		"location": "redis://default:secret@example.com:6380",
		"timeout":  "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.address != "example.com:6380" {
		t.Fatalf("address = %q", cfg.address)
	}
	if cfg.username != "default" || cfg.password != "secret" {
		t.Fatalf("credentials = %q/%q", cfg.username, cfg.password)
	}
	if cfg.timeout != 5*time.Second {
		t.Fatalf("timeout = %s", cfg.timeout)
	}
}

func TestParseRedisConfigPasswordOnlyURI(t *testing.T) {
	cfg, err := parseRedisConfig("redis", map[string]string{"location": "redis://secret@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.username != "" || cfg.password != "secret" {
		t.Fatalf("credentials = %q/%q", cfg.username, cfg.password)
	}
	if cfg.address != "example.com:6379" {
		t.Fatalf("address = %q", cfg.address)
	}
}

func TestParseRedisConfigTLS(t *testing.T) {
	cfg, err := parseRedisConfig("rediss", map[string]string{"location": "rediss://cache.example.com:6380"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.tls {
		t.Fatal("expected TLS to be enabled")
	}
	if cfg.tlsServerName != "cache.example.com" {
		t.Fatalf("tls server name = %q", cfg.tlsServerName)
	}
}

func TestFileURLPath(t *testing.T) {
	u, err := url.Parse("redis+file:///tmp/redis/dump.rdb")
	if err != nil {
		t.Fatal(err)
	}
	if got := fileURLPath(u); got != filepath.FromSlash("/tmp/redis/dump.rdb") {
		t.Fatalf("path = %q", got)
	}
}

func TestTargetPath(t *testing.T) {
	got := targetPath(filepath.FromSlash("/tmp/redis/"), "/dump.rdb")
	want := filepath.FromSlash("/tmp/redis/dump.rdb")
	if got != want {
		t.Fatalf("target = %q, want %q", got, want)
	}
	got = targetPath(filepath.FromSlash("/tmp/redis.rdb"), "/dump.rdb")
	want = filepath.FromSlash("/tmp/redis.rdb")
	if got != want {
		t.Fatalf("target = %q, want %q", got, want)
	}
}

func TestCopyWithContext(t *testing.T) {
	var dst bytes.Buffer
	n, err := copyWithContext(context.Background(), &dst, bytes.NewBufferString("redis"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || dst.String() != "redis" {
		t.Fatalf("copy = %d/%q", n, dst.String())
	}
}

func TestSendCommand(t *testing.T) {
	var buf bytes.Buffer
	if err := sendCommand(&buf, "AUTH", "default", "secret"); err != nil {
		t.Fatal(err)
	}
	want := "*3\r\n$4\r\nAUTH\r\n$7\r\ndefault\r\n$6\r\nsecret\r\n"
	if buf.String() != want {
		t.Fatalf("command = %q, want %q", buf.String(), want)
	}
}

func TestRDBReaderClosesConnection(t *testing.T) {
	conn := &fakeConn{}
	r := &rdbReader{conn: conn, reader: bytes.NewBufferString("abc"), remaining: 3}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Fatalf("data = %q", data)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !conn.closed {
		t.Fatal("close not called")
	}
}

type fakeConn struct{ closed bool }

func (f *fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (f *fakeConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (f *fakeConn) Close() error                     { f.closed = true; return nil }
func (f *fakeConn) LocalAddr() net.Addr              { return netAddr("local") }
func (f *fakeConn) RemoteAddr() net.Addr             { return netAddr("remote") }
func (f *fakeConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type netAddr string

func (a netAddr) Network() string { return string(a) }
func (a netAddr) String() string  { return string(a) }

func TestRDBReaderReportsShortStream(t *testing.T) {
	conn := &fakeConn{}
	r := &rdbReader{conn: conn, reader: bytes.NewBufferString("ab"), remaining: 3}
	_, err := io.ReadAll(r)
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("err = %v, want %v", err, io.ErrUnexpectedEOF)
	}
}
func TestOpenRDBWithFakeRedis(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		args, err := readTestCommand(br)
		if err != nil {
			done <- err
			return
		}
		if strings.Join(args, " ") != "PSYNC ? -1" {
			done <- io.ErrUnexpectedEOF
			return
		}
		_, err = conn.Write([]byte("+FULLRESYNC 0000000000000000000000000000000000000000 0\r\n$5\r\nREDIS"))
		done <- err
	}()

	c := &connector{cfg: config{address: ln.Addr().String(), timeout: time.Second}, proto: "redis"}
	r, err := c.openRDB(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if string(data) != "REDIS" {
		t.Fatalf("RDB data = %q", data)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func readTestCommand(br *bufio.Reader) ([]string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	n, err := strconv.Atoi(strings.TrimPrefix(line, "*"))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		size, err := strconv.Atoi(strings.TrimPrefix(line, "$"))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}
