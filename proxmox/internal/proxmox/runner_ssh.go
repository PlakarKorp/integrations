/*
 * Copyright (c) 2026 Gilles DUBOIS
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

package proxmox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

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
	defer func() {
		_ = session.Close()
	}()

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

func (r *SSHRunner) Stream(ctx context.Context, name string, args ...string) (*CommandStream, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}

	cmd := shellCommand(name, args...)
	if err := session.Start(cmd); err != nil {
		_ = session.Close()
		return nil, err
	}

	go func() {
		<-ctx.Done()
		_ = session.Close()
	}()

	return &CommandStream{
		Stdout: stdout,
		Stderr: stderr,
		finish: func() error {
			err := session.Wait()
			_ = session.Close()
			return err
		},
		abort: func() error {
			return session.Close()
		},
	}, nil
}

func (r *SSHRunner) Open(ctx context.Context, filepath string) (io.ReadCloser, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}

	var stderr bytes.Buffer
	session.Stderr = &stderr

	cmd := fmt.Sprintf("cat -- %s", shellQuote(filepath))
	if err := session.Start(cmd); err != nil {
		_ = session.Close()
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
		_ = session.Close()
		return nil, err
	}

	var stderr bytes.Buffer
	session.Stderr = &stderr

	cmd := fmt.Sprintf("cat > %s", shellQuote(filepath))
	if err := session.Start(cmd); err != nil {
		_ = stdin.Close()
		_ = session.Close()
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
