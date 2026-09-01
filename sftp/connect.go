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

package sftp

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

func controlDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate a private cache directory: %w", err)
	}

	dir := filepath.Join(base, "plakar", "ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}

	// MkdirAll is happy with a directory that already exists, whoever owns
	// it and whatever its mode is, so check what we ended up with.
	if err := checkPrivateDir(dir); err != nil {
		return "", err
	}

	return dir, nil
}

func controlSock(endpoint *url.URL, params map[string]string) (string, error) {
	dir, err := controlDir()
	if err != nil {
		return "", err
	}

	key := endpoint.String() + "|" + params["username"] + "|" + params["identity"]
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, fmt.Sprintf("%x.sock", sum[:8])), nil
}

// guard master creation per ControlPath
var masterMu sync.Map // map[string]*sync.Mutex
func lockFor(sock string) *sync.Mutex {
	m, _ := masterMu.LoadOrStore(sock, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func setupPrivateKey(params map[string]string) error {
	key := params["ssh_private_key"]
	if key == "" {
		return nil
	}

	ttl := params["ssh_private_key_ttl"]
	if ttl == "" {
		ttl = "5s"
	}

	cmd := exec.Command("ssh-add", "-t", ttl, "-")
	if sshAuthSock := params["ssh_auth_sock"]; sshAuthSock != "" {
		cmd.Env = append(cmd.Environ(), "SSH_AUTH_SOCK="+sshAuthSock)
	}

	cmd.Stdin = strings.NewReader(key + "\n")

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add key: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func sshArgs(endpoint *url.URL, params map[string]string) []string {
	// Non-interactive: fail fast instead of hanging on passphrase/host-key prompt
	args := []string{"-o", "BatchMode=yes"}

	if params["insecure_ignore_host_key"] == "true" {
		args = append(args, "-o", "StrictHostKeyChecking=no")
		// args = append(args, "-o", "UserKnownHostsFile=/dev/null") ?
	}

	if id := params["identity"]; id != "" {
		args = append(args, "-i", id)
	}

	if endpoint.User != nil {
		args = append(args, "-l", endpoint.User.Username())
	} else if params["username"] != "" {
		args = append(args, "-l", params["username"])
	}

	if p := endpoint.Port(); p != "" {
		args = append(args, "-p", p)
	}

	return args
}

func checkMaster(endpoint *url.URL, params map[string]string, host, sock string) error {
	args := sshArgs(endpoint, params)
	args = append(args, "-S", sock, "-O", "check", "--", host)

	out, err := exec.Command("ssh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh master not up: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func startMaster(endpoint *url.URL, params map[string]string, host, sock string) error {
	args := sshArgs(endpoint, params)
	args = append(args,
		"-N", "-f", "-S", sock,
		"-o", "ControlMaster=yes",
		"-o", "ControlPersist=10m",
		"--", host,
	)

	cmd := exec.Command("ssh", args...)
	if sshAuthSock := params["ssh_auth_sock"]; sshAuthSock != "" {
		cmd.Env = append(cmd.Environ(), "SSH_AUTH_SOCK="+sshAuthSock)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start ssh master: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ensureMaster(endpoint *url.URL, params map[string]string, host string) (string, error) {
	sock, err := controlSock(endpoint, params)
	if err != nil {
		return "", err
	}

	// Serialize master startup per socket
	mu := lockFor(sock)
	mu.Lock()
	defer mu.Unlock()

	if err := checkMaster(endpoint, params, host, sock); err == nil {
		return sock, nil
	}

	// add the private key to the agent if necessary
	if err := setupPrivateKey(params); err != nil {
		return "", fmt.Errorf("failed to set private key: %w", err)
	}

	if err := startMaster(endpoint, params, host, sock); err != nil {
		return "", err
	}

	if err := checkMaster(endpoint, params, host, sock); err != nil {
		return "", err
	}

	return sock, nil
}

func dial(args []string) (*sftp.Client, error) {
	cmd := exec.Command("ssh", args...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// misleading name: Wait() still waits for the process to
	// exit, but then it puts a 2 seconds limit for grandchildren
	// processes to close the pipes etc... that otherwise would
	// block.  think of a proxycommand for example.
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	client, err := sftp.NewClientPipe(stdout, stdin)
	if err != nil {
		// ssh should already be dead, but make sure it is
		// anyway.
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("ssh failed: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("ssh failed: %w", err)
	}

	return client, nil
}

func checkParamSupportForWindows(params map[string]string) error {
	for _, key := range []string{"ssh_auth_sock", "ssh_private_key", "ssh_private_key_ttl"} {
		if val := params[key]; val != "" {
			return fmt.Errorf("%q not supported on Windows", key)
		}
	}

	return nil
}

func connect(endpoint *url.URL, params map[string]string) (*sftp.Client, error) {
	if endpoint == nil {
		return nil, fmt.Errorf("nil endpoint")
	}

	host := endpoint.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing hostname in endpoint: %q", endpoint.String())
	}

	args := sshArgs(endpoint, params)

	// don't use the ControlMaster on windows
	if runtime.GOOS == "windows" {
		if err := checkParamSupportForWindows(params); err != nil {
			return nil, err
		}

		args = append(args, "-s", "--", host, "sftp")

		return dial(args)
	}

	// ensure the master exists (idempotent) and get the control socket path.
	sock, err := ensureMaster(endpoint, params, host)
	if err != nil {
		return nil, err
	}

	// reuse the master
	args = append(args, "-S", sock, "-s", "--", host, "sftp")

	return dial(args)
}
