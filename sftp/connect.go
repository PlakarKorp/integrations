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
	"bufio"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/pkg/sftp"
)

func controlSock(endpoint *url.URL, params map[string]string) string {
	key := endpoint.String() + "|" + params["username"] + "|" + params["identity"]
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(os.TempDir(), fmt.Sprintf("plakar-ssh-%x.sock", sum[:8]))
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
	args = append(args, "-S", sock, "-O", "check", host)

	out, err := exec.Command("ssh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh master not up: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func startMaster(endpoint *url.URL, params map[string]string, host, sock string) error {
	args := sshArgs(endpoint, params)
	args = append(args,
		"-N", "-f",
		"-o", "ControlMaster=yes",
		"-o", "ControlPersist=10m",
		"-o", "ControlPath="+sock,
		host,
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

func ensureMaster(endpoint *url.URL, params map[string]string) (string, error) {
	host := endpoint.Hostname()
	if host == "" {
		return "", fmt.Errorf("missing hostname in endpoint: %q", endpoint.String())
	}

	sock := controlSock(endpoint, params)

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

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	var sshErr error
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "Warning:") {
				continue
			}
			sshErr = fmt.Errorf("ssh command error: %q", line)
		}
	}()

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

	// reap process
	go func() { _ = cmd.Wait() }()

	client, err := sftp.NewClientPipe(stdout, stdin)
	if err != nil {
		if sshErr != nil {
			return nil, sshErr
		}
		return nil, err
	}

	return client, nil
}

func checkParamSupportForWindows(params map[string]string) error {
	for _, key := range []string{"ssh_auth_sock", "ssh_private_key", "ssh_private_key_ttl"} {
		if _, exists := params[key]; exists {
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

		args = append(args, host)
		args = append(args, "-s", "sftp")

		return dial(args)
	}

	// ensure the master exists (idempotent) and get the control socket path.
	sock, err := ensureMaster(endpoint, params)
	if err != nil {
		return nil, err
	}

	// reuse the master
	args = append(args, "-S", sock)
	args = append(args, host)
	args = append(args, "-s", "sftp")

	return dial(args)
}
