/*
 * Copyright (c) 2026 Antoine Dheygers <antoine.dheygers@cryptoweb.fr>
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

// Package conn dials the Incus daemon for both the importer and the
// exporter: over the local unix socket by default, or over HTTPS with
// TLS client-certificate authentication when the "url" option is set.
package conn

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"

	incus "github.com/lxc/incus/v6/client"
)

// DefaultSocket is where the Incus daemon listens locally.
const DefaultSocket = "/var/lib/incus/unix.socket"

// tlsOptions are the config keys that only make sense together with
// "url": on the unix-socket path they would be silently ignored, which
// hides a config mistake, so Connect rejects them instead.
var tlsOptions = []string{"tls_client_cert", "tls_client_key", "tls_server_cert", "tls_ca"}

// Connect returns an InstanceServer for the daemon described by the
// config map, scoped to config["project"] when non-empty. All option
// validation happens before any dial attempt.
func Connect(config map[string]string) (incus.InstanceServer, error) {
	var server incus.InstanceServer
	if url := config["url"]; url != "" {
		args, err := remoteArgs(config)
		if err != nil {
			return nil, err
		}
		server, err = incus.ConnectIncus(url, args)
		if err != nil {
			return nil, fmt.Errorf("incus: connect %s: %w", url, err)
		}
	} else {
		for _, key := range tlsOptions {
			if config[key] != "" {
				return nil, fmt.Errorf("incus: option %s is only valid together with url", key)
			}
		}
		socket := config["socket"]
		if socket == "" {
			socket = DefaultSocket
		}
		var err error
		server, err = incus.ConnectIncusUnix(socket, nil)
		if err != nil {
			return nil, fmt.Errorf("incus: connect %s: %w", socket, err)
		}
	}
	if project := config["project"]; project != "" {
		// Scope every subsequent API call to the given Incus project;
		// without this only the "default" project is visible.
		server = server.UseProject(project)
	}
	return server, nil
}

// remoteArgs validates the remote-connection options and loads the PEM
// material referenced by the tls_* paths. The client certificate/key
// pair is checked here so a broken pair fails with the option names
// rather than as an opaque TLS handshake error later.
func remoteArgs(config map[string]string) (*incus.ConnectionArgs, error) {
	url := config["url"]
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("incus: invalid url %q: must start with https://", url)
	}
	if config["socket"] != "" {
		return nil, errors.New(`incus: options "socket" and "url" are mutually exclusive`)
	}
	if config["tls_client_cert"] == "" || config["tls_client_key"] == "" {
		return nil, errors.New("incus: url requires both tls_client_cert and tls_client_key (paths to the PEM client certificate and key trusted by the remote daemon)")
	}
	cert, err := readPEM(config, "tls_client_cert")
	if err != nil {
		return nil, err
	}
	key, err := readPEM(config, "tls_client_key")
	if err != nil {
		return nil, err
	}
	if _, err := tls.X509KeyPair(cert, key); err != nil {
		return nil, fmt.Errorf("incus: tls_client_cert/tls_client_key do not form a valid pair: %w", err)
	}
	args := &incus.ConnectionArgs{
		TLSClientCert: string(cert),
		TLSClientKey:  string(key),
	}
	// Optional: pin the (usually self-signed) server certificate; without
	// it the system CA store must trust the server.
	if serverCert, err := readPEM(config, "tls_server_cert"); err != nil {
		return nil, err
	} else if serverCert != nil {
		args.TLSServerCert = string(serverCert)
	}
	// Optional: CA certificate for daemons running in PKI mode.
	if ca, err := readPEM(config, "tls_ca"); err != nil {
		return nil, err
	} else if ca != nil {
		args.TLSCA = string(ca)
	}
	return args, nil
}

// readPEM reads the file referenced by the given config key, or returns
// (nil, nil) when the key is absent.
func readPEM(config map[string]string, key string) ([]byte, error) {
	path, ok := config[key]
	if !ok || path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("incus: %s: %w", key, err)
	}
	return data, nil
}
