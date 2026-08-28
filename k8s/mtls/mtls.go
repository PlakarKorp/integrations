package mtls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"
)

var (
	ErrNoCertificate  = errors.New("no certificate presented")
	ErrMismatch       = errors.New("peer key mismatch")
	ErrBadFingerprint = errors.New("bad fingerprint")
)

func Gencert() (tls.Certificate, [32]byte, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, [32]byte{}, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "plakar"},

		// not enforced
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(24 * time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, [32]byte{}, err
	}

	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return tls.Certificate{}, [32]byte{}, err
	}

	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	return cert, sha256.Sum256(spki), nil
}

func Pinned(want [32]byte) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return ErrNoCertificate
		}
		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return err
		}
		if sha256.Sum256(cert.RawSubjectPublicKeyInfo) != want {
			return ErrMismatch
		}
		return nil
	}
}

func Fingerprint(fp [32]byte) string {
	return hex.EncodeToString(fp[:])
}

func ParseFingerprint(s string) ([32]byte, error) {
	var fp [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return fp, fmt.Errorf("%w: %w", ErrBadFingerprint, err)
	}
	if len(b) != len(fp) {
		return fp, fmt.Errorf("%w: got %d bytes, want %d",
			ErrBadFingerprint, len(b), len(fp))
	}
	copy(fp[:], b)
	return fp, nil
}

func ServerTlsConfig(cert tls.Certificate, peer [32]byte) *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{cert},
		ClientAuth:            tls.RequireAnyClientCert,
		VerifyPeerCertificate: Pinned(peer),
		MinVersion:            tls.VersionTLS13,
		NextProtos:            []string{"h2"},
	}
}

func ClientTlsConfig(cert *tls.Certificate) *tls.Config {
	cfg := &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return cfg
}
