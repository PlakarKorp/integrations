package common

import (
	"bufio"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/pkg/sftp"
)

func Connect(endpoint *url.URL, params map[string]string) (*sftp.Client, error) {
	var args []string

	// Due to the agent, we can't have anything interactive right now (password/known host check etc)
	// so disable them to fail early and in a meaningful way.
	args = append(args, "-o", "BatchMode=yes")

	if params["insecure_ignore_host_key"] == "true" {
		args = append(args, "-o", "StrictHostKeyChecking=no")
	}

	if id := params["identity"]; id != "" {
		args = append(args, "-i", id)
	}

	if endpoint.User != nil && params["username"] != "" {
		return nil, fmt.Errorf("can not use user@host foo syntax and username parameter.")
	} else if endpoint.User != nil {
		args = append(args, "-l", endpoint.User.Username())
	} else if params["username"] != "" {
		args = append(args, "-l", params["username"])
	}

	if endpoint.Port() != "" {
		args = append(args, "-p", endpoint.Port())
	}

	args = append(args, endpoint.Hostname())

	// This one must be after the host, tell the ssh command to load the sftp subsystem
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
			if strings.HasPrefix(sc.Text(), "Warning:") {
				continue
			}

			sshErr = fmt.Errorf("ssh command error: %q", sc.Text())
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

	go func() {
		cmd.Wait()
	}()

	client, err := sftp.NewClientPipe(stdout, stdin)
	if err != nil {
		if sshErr != nil {
			return nil, sshErr
		} else {
			return nil, err
		}
	}

	return client, nil
}
