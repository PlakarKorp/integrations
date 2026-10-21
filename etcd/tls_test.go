package etcd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// selfSigned writes a self-signed certificate and its key to dir, and returns
// their paths.
func selfSigned(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestEtcdTLS(t *testing.T) {
	certFile, keyFile := selfSigned(t, t.TempDir())

	// Nothing configured: leave the clientv3 default in place.
	cfg, err := etcdTLS(map[string]string{})
	if err != nil || cfg != nil {
		t.Errorf("etcdTLS({}) = %v, %v; want nil, nil", cfg, err)
	}

	cfg, err = etcdTLS(map[string]string{"tls_insecure_no_verify": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.InsecureSkipVerify {
		t.Error("tls_insecure_no_verify=true did not disable verification")
	}

	cfg, err = etcdTLS(map[string]string{"ca_file": certFile})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootCAs == nil {
		t.Error("ca_file did not set the root CAs")
	}
	if cfg.InsecureSkipVerify {
		t.Error("ca_file disabled verification")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}

	cfg, err = etcdTLS(map[string]string{"cert_file": certFile, "key_file": keyFile})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("cert_file and key_file gave %d certificates, want 1", len(cfg.Certificates))
	}
}

func TestEtcdTLSInvalid(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := selfSigned(t, dir)
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}

	for name, config := range map[string]map[string]string{
		"invalid tls_insecure_no_verify": {"tls_insecure_no_verify": "maybe"},
		"missing ca_file":                {"ca_file": filepath.Join(dir, "missing")},
		"ca_file without a certificate":  {"ca_file": empty},
		"cert_file without key_file":     {"cert_file": certFile},
		"key_file without cert_file":     {"key_file": keyFile},
		"mismatched key_file":            {"cert_file": certFile, "key_file": certFile},
	} {
		if _, err := etcdTLS(config); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
