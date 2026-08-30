package routeros

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyCallback builds the host key verification for a RouterOS connection.
//
// This used to be ssh.InsecureIgnoreHostKey() with an "// XXX" and no way to
// change it, so every backup accepted whatever key the far end presented and
// anyone on path could collect the device credentials.
//
// Three ways to say what we expect, in order of precedence:
//
//	host_key                  a pinned key, in authorized_keys line format
//	known_hosts               path to a known_hosts file (default ~/.ssh/known_hosts)
//	insecure_ignore_host_key  accept anything, as before, but on purpose
func hostKeyCallback(config map[string]string) (ssh.HostKeyCallback, error) {
	if v, ok := config["insecure_ignore_host_key"]; ok && v != "" {
		ignore, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid insecure_ignore_host_key value %q", v)
		}
		if ignore {
			return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // opt-in
		}
	}

	if pinned := strings.TrimSpace(config["host_key"]); pinned != "" {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pinned))
		if err != nil {
			return nil, fmt.Errorf("invalid host_key: %w", err)
		}
		return ssh.FixedHostKey(key), nil
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
				"or set host_key, known_hosts, or insecure_ignore_host_key=true", path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	// Wrap so an unknown host says what to do about it rather than surfacing
	// the library's bare "knownhosts: key is unknown".
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := cb(hostname, remote, key); err != nil {
			return fmt.Errorf("%w (host key for %s is %s; add it to %s, "+
				"pin it with host_key, or set insecure_ignore_host_key=true)",
				err, hostname, ssh.FingerprintSHA256(key), path)
		}
		return nil
	}, nil
}
