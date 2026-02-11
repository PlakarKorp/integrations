package proxmox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, string, error)
	Open(ctx context.Context, filepath string) (io.ReadCloser, error)
	Create(ctx context.Context, filepath string) (io.WriteCloser, error)
	Stat(ctx context.Context, filepath string) (os.FileInfo, error)
	Remove(ctx context.Context, filepath string) error
	Close() error
}

func NewRunner(cfg *Config) (Runner, error) {
	if cfg.Mode == ModeLocal {
		return &LocalRunner{}, nil
	}
	return NewSSHRunner(cfg)
}

type LocalRunner struct{}

func (r *LocalRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	return stdout.String(), stderr.String(), cmd.Run()
}

func (r *LocalRunner) Open(ctx context.Context, filepath string) (io.ReadCloser, error) {
	return os.Open(filepath)
}

func (r *LocalRunner) Create(ctx context.Context, filepath string) (io.WriteCloser, error) {
	return os.Create(filepath)
}

func (r *LocalRunner) Stat(ctx context.Context, filepath string) (os.FileInfo, error) {
	return os.Stat(filepath)
}

func (r *LocalRunner) Remove(ctx context.Context, filepath string) error {
	return os.Remove(filepath)
}

func (r *LocalRunner) Close() error {
	return nil
}

type SSHRunner struct {
	client *ssh.Client
}

func NewSSHRunner(cfg *Config) (*SSHRunner, error) {
	if cfg.ConnUsername == "" {
		return nil, fmt.Errorf("missing conn_username")
	}

	var auth ssh.AuthMethod
	switch cfg.ConnMethod {
	case ConnMethodPassword:
		auth = ssh.Password(cfg.ConnPassword)
	case ConnMethodIdentity:
		key, err := os.ReadFile(cfg.ConnIdentityFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read identity file: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("failed to parse identity file: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	default:
		return nil, fmt.Errorf("unsupported conn_method: %s", cfg.ConnMethod)
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.ConnUsername,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	addr := normalizeSSHAddr(cfg.Host)
	client, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial failed: %w", err)
	}

	return &SSHRunner{client: client}, nil
}

func (r *SSHRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return "", "", err
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	cmd := shellCommand(name, args...)
	go func() {
		<-ctx.Done()
		_ = session.Close()
	}()

	err = session.Run(cmd)
	return stdout.String(), stderr.String(), err
}

func (r *SSHRunner) Open(ctx context.Context, filepath string) (io.ReadCloser, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, err
	}

	var stderr bytes.Buffer
	session.Stderr = &stderr

	cmd := fmt.Sprintf("cat -- %s", shellQuote(filepath))
	if err := session.Start(cmd); err != nil {
		session.Close()
		return nil, err
	}

	return &sshReadCloser{
		session: session,
		stdout:  stdout,
		stderr:  &stderr,
	}, nil
}

func (r *SSHRunner) Create(ctx context.Context, filepath string) (io.WriteCloser, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, err
	}

	var stderr bytes.Buffer
	session.Stderr = &stderr

	cmd := fmt.Sprintf("cat > %s", shellQuote(filepath))
	if err := session.Start(cmd); err != nil {
		_ = stdin.Close()
		session.Close()
		return nil, err
	}

	return &sshWriteCloser{
		session: session,
		stdin:   stdin,
		stderr:  &stderr,
	}, nil
}

func (r *SSHRunner) Stat(ctx context.Context, filepath string) (os.FileInfo, error) {
	stdout, stderr, err := r.Run(ctx, "stat", "-c", "%s %Y", "--", filepath)
	if err != nil {
		return nil, fmt.Errorf("stat failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	fields := strings.Fields(stdout)
	if len(fields) < 2 {
		return nil, fmt.Errorf("unexpected stat output: %s", strings.TrimSpace(stdout))
	}

	size, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid stat size: %w", err)
	}

	mtime, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid stat mtime: %w", err)
	}

	return &remoteFileInfo{
		name:    path.Base(filepath),
		size:    size,
		mode:    0600,
		modTime: time.Unix(mtime, 0),
	}, nil
}

func (r *SSHRunner) Remove(ctx context.Context, filepath string) error {
	_, stderr, err := r.Run(ctx, "rm", "-f", "--", filepath)
	if err != nil {
		return fmt.Errorf("rm failed: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

func (r *SSHRunner) Close() error {
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

type remoteFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
}

func (fi *remoteFileInfo) Name() string       { return fi.name }
func (fi *remoteFileInfo) Size() int64        { return fi.size }
func (fi *remoteFileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *remoteFileInfo) ModTime() time.Time { return fi.modTime }
func (fi *remoteFileInfo) IsDir() bool        { return fi.mode.IsDir() }
func (fi *remoteFileInfo) Sys() any           { return nil }

type sshReadCloser struct {
	session *ssh.Session
	stdout  io.Reader
	stderr  *bytes.Buffer
	closed  bool
}

func (r *sshReadCloser) Read(p []byte) (int, error) {
	return r.stdout.Read(p)
}

func (r *sshReadCloser) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true

	err := r.session.Wait()
	_ = r.session.Close()
	if err != nil {
		return fmt.Errorf("remote read failed: %w: %s", err, strings.TrimSpace(r.stderr.String()))
	}
	return nil
}

type sshWriteCloser struct {
	session *ssh.Session
	stdin   io.WriteCloser
	stderr  *bytes.Buffer
	closed  bool
}

func (w *sshWriteCloser) Write(p []byte) (int, error) {
	return w.stdin.Write(p)
}

func (w *sshWriteCloser) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true

	errClose := w.stdin.Close()
	errWait := w.session.Wait()
	_ = w.session.Close()

	if errClose != nil {
		return errClose
	}
	if errWait != nil {
		return fmt.Errorf("remote write failed: %w: %s", errWait, strings.TrimSpace(w.stderr.String()))
	}
	return nil
}

func normalizeSSHAddr(host string) string {
	if host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, "22")
}

func shellCommand(name string, args ...string) string {
	parts := append([]string{name}, args...)
	for i, part := range parts {
		parts[i] = shellQuote(part)
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
