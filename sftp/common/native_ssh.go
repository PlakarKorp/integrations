package common

import (
	"fmt"
	"net"
	"net/url"
	"os"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// nativeSFTPClient is a wrapper around *sftp.Client that ensures the ssh connection
// is closed when the sftp client is closed.
type nativeSFTPClient struct {
	*sftp.Client
	conn *ssh.Client
}

func (c *nativeSFTPClient) Close() error {
	err := c.Client.Close()
	if cerr := c.conn.Close(); err == nil {
		err = cerr
	}
	return err
}

func buildAuthMethods(params map[string]string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if key := params["ssh_private_key"]; key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("failed to parse ssh_private_key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if id := params["identity"]; id != "" {
		data, err := os.ReadFile(id)
		if err != nil {
			return nil, fmt.Errorf("failed to read identity file: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse identity file: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if sock := params["ssh_auth_sock"]; sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			ag := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no authentication method available (set identity, ssh_private_key, or ssh_auth_sock)")
	}

	return methods, nil
}

func hostKeyCallback(params map[string]string) (ssh.HostKeyCallback, error) {
	return ssh.InsecureIgnoreHostKey(), nil
}

// connectNativeSSH connects to the remote server using the native SSH client
func connectNativeSSH(endpoint *url.URL, params map[string]string) (*sftp.Client, error) {
	user, err := resolveUsername(endpoint, params)
	if err != nil {
		return nil, err
	}

	auth, err := buildAuthMethods(params)
	if err != nil {
		return nil, err
	}

	hkcb, err := hostKeyCallback(params)
	if err != nil {
		return nil, err
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: hkcb,
	}

	addr := endpoint.Hostname()
	if p := endpoint.Port(); p != "" {
		addr += ":" + p
	} else {
		addr += ":22"
	}

	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}

	client, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}

	return client, nil
}
