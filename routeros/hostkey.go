package routeros

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyCallback verifies the device against known_hosts (default
// ~/.ssh/known_hosts) unless insecure_ignore_host_key is set.
func hostKeyCallback(config map[string]string) (ssh.HostKeyCallback, error) {
	if v, ok := config["insecure_ignore_host_key"]; ok && v != "" {
		ignore, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid insecure_ignore_host_key value %q", v)
		}
		if ignore {
			return ssh.InsecureIgnoreHostKey(), nil
		}
	}

	path := config["known_hosts"]
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot locate known_hosts: %w", err)
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}

	cb, err := knownhosts.New(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s does not exist: add the device's key to it, "+
				"or set known_hosts or insecure_ignore_host_key=true", path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	// Wrap so an unknown host says what to do about it rather than surfacing
	// the library's bare "knownhosts: key is unknown".
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := cb(hostname, remote, key); err != nil {
			return fmt.Errorf("%w (host key for %s is %s; add it to %s, "+
				"or set insecure_ignore_host_key=true)",
				err, hostname, ssh.FingerprintSHA256(key), path)
		}
		return nil
	}, nil
}
