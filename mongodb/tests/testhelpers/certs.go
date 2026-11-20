package testhelpers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

// Paths of the generated certificates inside the containers.
const (
	CACertPath     = "/certs/ca.pem"
	ServerPEMPath  = "/certs/server.pem"
	ClientPEMPath  = "/certs/client.pem"
	certsFileMode  = 0o644
	certsValidity  = 24 * time.Hour
	serverOrg      = "PlakarTestServer"
	clientOrg      = "PlakarTestClient"
	clientCommonNm = "plakar"
)

// Certs holds a test CA, and a server and a client certificate signed by it.
// The server and client PEMs hold the certificate followed by its key, as
// mongod and the MongoDB tools expect.
type Certs struct {
	CA     []byte
	Server []byte
	Client []byte
	// ClientSubject is the client certificate's subject, which is the user
	// name for MONGODB-X509 authentication.
	ClientSubject string
}

// GenerateCerts creates Certs whose server certificate is valid for hosts
// and for localhost.
func GenerateCerts(t *testing.T, hosts ...string) *Certs {
	t.Helper()

	caKey := newKey(t)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Plakar Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certsValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}

	// mongod treats a client whose O/OU/DC match its own as a cluster
	// member, so the two certificates use different organisations.
	server := issue(t, ca, caKey, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost", Organization: []string{serverOrg}},
		DNSNames:     append([]string{"localhost"}, hosts...),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	clientSubject := pkix.Name{CommonName: clientCommonNm, Organization: []string{clientOrg}}
	client := issue(t, ca, caKey, &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      clientSubject,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})

	return &Certs{
		CA:            pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		Server:        server,
		Client:        client,
		ClientSubject: clientSubject.String(),
	}
}

// Files returns the certificates as files to copy into a container.
func (c *Certs) Files() []testcontainers.ContainerFile {
	return []testcontainers.ContainerFile{
		{Reader: bytes.NewReader(c.CA), ContainerFilePath: CACertPath, FileMode: certsFileMode},
		{Reader: bytes.NewReader(c.Server), ContainerFilePath: ServerPEMPath, FileMode: certsFileMode},
		{Reader: bytes.NewReader(c.Client), ContainerFilePath: ClientPEMPath, FileMode: certsFileMode},
	}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// issue signs tmpl with the CA and returns the certificate and its key as PEM.
func issue(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, tmpl *x509.Certificate) []byte {
	t.Helper()
	tmpl.NotBefore = ca.NotBefore
	tmpl.NotAfter = ca.NotAfter
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature

	key := newKey(t)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create certificate %s: %v", tmpl.Subject.CommonName, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key %s: %v", tmpl.Subject.CommonName, err)
	}
	return append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
}
