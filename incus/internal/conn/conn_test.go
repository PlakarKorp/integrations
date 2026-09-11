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

package conn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCertPair writes a fresh self-signed certificate and key as PEM
// files under dir and returns their paths.
func writeCertPair(t *testing.T, dir, prefix string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "plakar-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, prefix+"-cert.pem")
	keyPath = filepath.Join(dir, prefix+"-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestRemoteArgs(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCertPair(t, dir, "client")

	args, err := remoteArgs(map[string]string{
		"url":             "https://incus.example:8443",
		"tls_client_cert": certPath,
		"tls_client_key":  keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCert, _ := os.ReadFile(certPath)
	wantKey, _ := os.ReadFile(keyPath)
	if args.TLSClientCert != string(wantCert) {
		t.Error("TLSClientCert is not the certificate file content")
	}
	if args.TLSClientKey != string(wantKey) {
		t.Error("TLSClientKey is not the key file content")
	}
	if args.TLSServerCert != "" || args.TLSCA != "" {
		t.Error("server cert / CA should be empty when not configured")
	}
}

func TestRemoteArgsServerCertAndCA(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCertPair(t, dir, "client")
	serverCertPath, _ := writeCertPair(t, dir, "server")
	caPath, _ := writeCertPair(t, dir, "ca")

	args, err := remoteArgs(map[string]string{
		"url":             "https://incus.example:8443",
		"tls_client_cert": certPath,
		"tls_client_key":  keyPath,
		"tls_server_cert": serverCertPath,
		"tls_ca":          caPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantServer, _ := os.ReadFile(serverCertPath)
	wantCA, _ := os.ReadFile(caPath)
	if args.TLSServerCert != string(wantServer) {
		t.Error("TLSServerCert is not the file content")
	}
	if args.TLSCA != string(wantCA) {
		t.Error("TLSCA is not the file content")
	}
}

func TestRemoteArgsErrors(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCertPair(t, dir, "client")
	otherCert, _ := writeCertPair(t, dir, "other")

	cases := []struct {
		name    string
		config  map[string]string
		wantSub string
	}{
		{
			"http scheme rejected",
			map[string]string{"url": "http://incus.example:8443", "tls_client_cert": certPath, "tls_client_key": keyPath},
			"https://",
		},
		{
			"socket and url exclusive",
			map[string]string{"url": "https://incus.example:8443", "socket": "/run/incus.sock", "tls_client_cert": certPath, "tls_client_key": keyPath},
			"mutually exclusive",
		},
		{
			"missing client cert",
			map[string]string{"url": "https://incus.example:8443", "tls_client_key": keyPath},
			"tls_client_cert",
		},
		{
			"missing client key",
			map[string]string{"url": "https://incus.example:8443", "tls_client_cert": certPath},
			"tls_client_key",
		},
		{
			"unreadable cert file",
			map[string]string{"url": "https://incus.example:8443", "tls_client_cert": filepath.Join(dir, "nope.pem"), "tls_client_key": keyPath},
			"nope.pem",
		},
		{
			"mismatched cert/key pair",
			map[string]string{"url": "https://incus.example:8443", "tls_client_cert": otherCert, "tls_client_key": keyPath},
			"tls_client_cert",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := remoteArgs(tc.config); err == nil {
				t.Fatalf("expected error for %v", tc.config)
			} else if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// TLS options without url are a config mistake (they would be silently
// ignored on the unix-socket path): Connect must reject them before any
// dial attempt.
func TestConnectRejectsTLSOptionsWithoutURL(t *testing.T) {
	for _, key := range []string{"tls_client_cert", "tls_client_key", "tls_server_cert", "tls_ca"} {
		t.Run(key, func(t *testing.T) {
			_, err := Connect(map[string]string{key: "/some/path.pem", "socket": "/nonexistent.sock"})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "url") {
				t.Fatalf("error %q should name the option %q and mention url", err, key)
			}
		})
	}
}

func TestConnectUnixDialError(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")
	_, err := Connect(map[string]string{"socket": sock})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), sock) {
		t.Fatalf("error %q should mention the socket path", err)
	}
}

// End-to-end remote connect against a fake Incus API endpoint: HTTPS,
// server certificate pinned via tls_server_cert, client certificate
// supplied. ConnectIncus performs a GET /1.0 on connect, so a successful
// Connect proves the whole TLS plumbing.
func TestConnectRemote(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/1.0") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":        "sync",
			"status":      "Success",
			"status_code": 200,
			"metadata": map[string]any{
				"api_version": "1.0",
				"auth":        "trusted",
			},
		})
	}))
	defer ts.Close()

	dir := t.TempDir()
	certPath, keyPath := writeCertPair(t, dir, "client")
	serverCertPath := filepath.Join(dir, "server-cert.pem")
	serverPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	if err := os.WriteFile(serverCertPath, serverPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	server, err := Connect(map[string]string{
		"url":             ts.URL,
		"tls_client_cert": certPath,
		"tls_client_key":  keyPath,
		"tls_server_cert": serverCertPath,
		"project":         "prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if server == nil {
		t.Fatal("nil server")
	}
	// Without the pinned server certificate the self-signed test server
	// must be rejected by standard CA verification.
	if _, err := Connect(map[string]string{
		"url":             ts.URL,
		"tls_client_cert": certPath,
		"tls_client_key":  keyPath,
	}); err == nil {
		t.Fatal("expected TLS verification failure without tls_server_cert")
	}
}
