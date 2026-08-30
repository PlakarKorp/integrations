//go:build !windows

package common

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkg/sftp"
)

// controlDir returns a directory only this user can write to, in which the
// multiplexing sockets live.
//
// The socket path used to be derived from os.TempDir(), which is world
// writable and shared: every input to the name (endpoint, username, identity
// path) is knowable, so another local user could compute it and bind a socket
// there first.  ssh does not check who owns a control socket, so "-O check"
// would succeed against the squatter and every subsequent SFTP operation would
// be multiplexed through their process.
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
	if endpoint == nil {
		return "", fmt.Errorf("nil endpoint")
	}

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

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	go func() {
		defer stdin.Close()
		io.WriteString(stdin, key+"\n")
	}()

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add key: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func ensureMaster(endpoint *url.URL, params map[string]string) (string, error) {
	host := endpoint.Hostname()
	if err := checkHost(host); err != nil {
		return "", fmt.Errorf("%w: %q", err, endpoint.String())
	}

	sock, err := controlSock(endpoint, params)
	if err != nil {
		return "", err
	}

	// Serialize master startup per socket
	mu := lockFor(sock)
	mu.Lock()
	defer mu.Unlock()

	commonArgs := func() ([]string, error) {
		var args []string

		// Non-interactive: fail fast instead of hanging on passphrase/host-key prompt
		args = append(args, "-o", "BatchMode=yes")

		if params["insecure_ignore_host_key"] == "true" {
			args = append(args, "-o", "StrictHostKeyChecking=no")
			// args = append(args, "-o", "UserKnownHostsFile=/dev/null") ?
		}

		if id := params["identity"]; id != "" {
			args = append(args, "-i", id)
		}

		// Username resolution: forbid user@host + username param
		if endpoint.User != nil && params["username"] != "" {
			return nil, fmt.Errorf("can not use user@host syntax and username parameter")
		} else if endpoint.User != nil {
			args = append(args, "-l", endpoint.User.Username())
		} else if params["username"] != "" {
			args = append(args, "-l", params["username"])
		}

		if p := endpoint.Port(); p != "" {
			args = append(args, "-p", p)
		}

		return args, nil
	}

	// check existing master
	{
		args, err := commonArgs()
		if err != nil {
			return "", err
		}
		checkArgs := append([]string{}, args...)
		checkArgs = append(checkArgs, "-S", sock, "-O", "check", "--", host)

		if err := exec.Command("ssh", checkArgs...).Run(); err == nil {
			return sock, nil
		}
	}

	// add the private key to the agent if necessary
	if err := setupPrivateKey(params); err != nil {
		return "", fmt.Errorf("failed to set private key: %w", err)
	}

	// start master
	{
		args, err := commonArgs()
		if err != nil {
			return "", err
		}
		startArgs := append([]string{}, args...)
		startArgs = append(startArgs,
			"-M", "-N", "-f",
			"-o", "ControlMaster=yes",
			"-o", "ControlPersist=10m",
			"-o", "ControlPath="+sock,
			"--", host,
		)

		cmd := exec.Command("ssh", startArgs...)
		if sshAuthSock := params["ssh_auth_sock"]; sshAuthSock != "" {
			cmd.Env = append(cmd.Environ(), "SSH_AUTH_SOCK="+sshAuthSock)
		}

		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("failed to start ssh master: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}

	// verify
	{
		args, err := commonArgs()
		if err != nil {
			return "", err
		}
		checkArgs := append([]string{}, args...)
		checkArgs = append(checkArgs, "-S", sock, "-O", "check", "--", host)

		out, err := exec.Command("ssh", checkArgs...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("ssh master did not come up: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}

	return sock, nil
}

func Connect(endpoint *url.URL, params map[string]string) (*sftp.Client, error) {
	if endpoint == nil {
		return nil, fmt.Errorf("nil endpoint")
	}

	host := endpoint.Hostname()
	if err := checkHost(host); err != nil {
		return nil, fmt.Errorf("%w: %q", err, endpoint.String())
	}

	// ensure the master exists (idempotent) and get the control socket path.
	sock, err := ensureMaster(endpoint, params)
	if err != nil {
		return nil, err
	}

	var args []string

	args = append(args, "-o", "BatchMode=yes")

	if params["insecure_ignore_host_key"] == "true" {
		args = append(args, "-o", "StrictHostKeyChecking=no")
	}

	if id := params["identity"]; id != "" {
		args = append(args, "-i", id)
	}

	// username resolution: forbid both user@host AND username param
	if endpoint.User != nil && params["username"] != "" {
		return nil, fmt.Errorf("can not use user@host foo syntax and username parameter")
	} else if endpoint.User != nil {
		args = append(args, "-l", endpoint.User.Username())
	} else if params["username"] != "" {
		args = append(args, "-l", params["username"])
	}

	if p := endpoint.Port(); p != "" {
		args = append(args, "-p", p)
	}

	// reuse the master
	args = append(args, "-S", sock)
	args = append(args, "--", host)
	args = append(args, "-s", "sftp")

	cmd := exec.Command("ssh", args...)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	var (
		sshErrMu sync.Mutex
		sshErr   error
	)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "Warning:") {
				continue
			}
			sshErrMu.Lock()
			sshErr = fmt.Errorf("ssh command error: %q", line)
			sshErrMu.Unlock()
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
		sshErrMu.Lock()
		defer sshErrMu.Unlock()
		if sshErr != nil {
			return nil, sshErr
		}
		return nil, err
	}

	return client, nil
}
