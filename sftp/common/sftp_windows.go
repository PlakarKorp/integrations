package common

import (
	"bufio"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/pkg/sftp"
)

func CheckParamSupport(params map[string]string) error {
	for _, key := range []string{"ssh_auth_sock", "ssh_private_key", "ssh_private_key_ttl"} {
		if _, exists := params[key]; exists {
			return fmt.Errorf("%q not support on Windows", key)
		}
	}

	return nil
}

func Connect(endpoint *url.URL, params map[string]string) (*sftp.Client, error) {
	if endpoint == nil {
		return nil, fmt.Errorf("nil endpoint")
	}

	host := endpoint.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing hostname in endpoint: %q", endpoint.String())
	}

	if err := CheckParamSupport(params); err != nil {
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
