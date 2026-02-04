package common

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkg/sftp"
)

func controlSock(endpoint *url.URL, params map[string]string) (string, error) {
	if endpoint == nil {
		return "", fmt.Errorf("nil endpoint")
	}

	key := endpoint.String() + "|" + params["username"] + "|" + params["identity"]
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(os.TempDir(), fmt.Sprintf("plakar-ssh-%x.sock", sum[:8])), nil
}

// guard master creation per ControlPath
var masterMu sync.Map // map[string]*sync.Mutex
func lockFor(sock string) *sync.Mutex {
	m, _ := masterMu.LoadOrStore(sock, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func ensureMaster(endpoint *url.URL, params map[string]string) (string, error) {
	host := endpoint.Hostname()
	if host == "" {
		return "", fmt.Errorf("missing hostname in endpoint: %q", endpoint.String())
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
		checkArgs = append(checkArgs, "-S", sock, "-O", "check", host)

		if err := exec.Command("ssh", checkArgs...).Run(); err == nil {
			return sock, nil
		}
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
			host,
		)

		out, err := exec.Command("ssh", startArgs...).CombinedOutput()
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
		checkArgs = append(checkArgs, "-S", sock, "-O", "check", host)

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
	if host == "" {
		return nil, fmt.Errorf("missing hostname in endpoint: %q", endpoint.String())
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
	args = append(args, host)
	args = append(args, "-s", "sftp")

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
