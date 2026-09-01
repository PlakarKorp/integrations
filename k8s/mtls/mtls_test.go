package mtls

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
)

func gencert(t *testing.T) (tls.Certificate, [32]byte) {
	t.Helper()

	cert, fp, err := Gencert()
	require.NoError(t, err)
	return cert, fp
}

func TestGencertIsPerCall(t *testing.T) {
	_, a := gencert(t)
	_, b := gencert(t)

	require.NotEqual(t, a, b, "gencert generated the same fingerprint twice")
}

func TestGencertFingerprintMatchesCertificate(t *testing.T) {
	cert, fp := gencert(t)

	require.Equal(t, 1, len(cert.Certificate), "too many certs generated")

	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)

	require.Equal(t, sha256.Sum256(parsed.RawSubjectPublicKeyInfo), fp)
}

func TestGencertUsesEd25519(t *testing.T) {
	cert, _ := gencert(t)

	if _, ok := cert.PrivateKey.(ed25519.PrivateKey); !ok {
		t.Errorf("private key is %T, want ed25519.PrivateKey", cert.PrivateKey)
	}
}

func TestFingerprintRoundTrip(t *testing.T) {
	_, fp := gencert(t)

	s := Fingerprint(fp)
	require.Equal(t, 2*sha256.Size, len(s))

	back, err := ParseFingerprint(s)
	require.NoError(t, err)
	require.Equal(t, fp, back)
}

func TestParseFingerprint(t *testing.T) {
	valid := Fingerprint(sha256.Sum256([]byte("plakar")))

	for _, tt := range []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", valid, true},
		{"uppercase", strings.ToUpper(valid), true},
		{"empty", "", false},
		{"not hex", strings.Repeat("z", 64), false},
		{"odd length", valid[:63], false},
		{"too short", valid[:62], false},
		{"too long", valid + "00", false},
		{"whitespace", " " + valid, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFingerprint(tt.in)
			if tt.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "accepted an invalid fingerprint")
				require.ErrorIs(t, err, ErrBadFingerprint)
			}
		})
	}
}

func TestPinnedAcceptsMatchingKey(t *testing.T) {
	cert, fp := gencert(t)

	require.NoError(t, Pinned(fp)(cert.Certificate, nil))
}

func TestPinnedRejectsDifferentKey(t *testing.T) {
	_, pinned := gencert(t)
	other, _ := gencert(t)

	err := Pinned(pinned)(other.Certificate, nil)
	require.ErrorIs(t, err, ErrMismatch)
}

func TestPinnedRejectsNoCertificate(t *testing.T) {
	_, fp := gencert(t)

	require.ErrorIs(t, Pinned(fp)(nil, nil), ErrNoCertificate)
	require.ErrorIs(t, Pinned(fp)([][]byte{}, nil), ErrNoCertificate)
}

func TestPinnedRejectsUnparseableCertificate(t *testing.T) {
	_, fp := gencert(t)

	require.Error(t, Pinned(fp)([][]byte{[]byte("not a certificate")}, nil))
}

type pair struct {
	srvCert tls.Certificate
	srvPeer [32]byte         // the client key the server accepts
	cliCert *tls.Certificate // nil presents no certificate at all
	cliPeer [32]byte         // the server key the client accepts
}

func matching(t *testing.T) pair {
	t.Helper()

	srvCert, srvFP := gencert(t)
	cliCert, cliFP := gencert(t)

	return pair{srvCert: srvCert, srvPeer: cliFP, cliCert: &cliCert, cliPeer: srvFP}
}

// exchange run one handshake and returns what each side thought of
// the other.
func exchange(t *testing.T, e pair) (server, client error) {
	t.Helper()

	cconn, sconn := net.Pipe()
	defer cconn.Close()
	defer sconn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serrc := make(chan error, 1)
	go func() {
		c := tls.Server(sconn, ServerTlsConfig(e.srvCert, e.srvPeer))
		err := c.HandshakeContext(ctx)
		serrc <- err // report before reading, or the caller deadlocks
		if err == nil {
			c.SetReadDeadline(time.Now().Add(time.Second))
			var b [1]byte
			c.Read(b[:])
		}
	}()

	cerrc := make(chan error, 1)
	go func() {
		c := tls.Client(cconn, ClientTlsConfig(e.cliCert, e.cliPeer))
		err := c.HandshakeContext(ctx)
		cerrc <- err
		if err == nil {
			// keep reading to get the close_notify
			c.SetReadDeadline(time.Now().Add(time.Second))
			var b [1]byte
			c.Read(b[:])
		}
	}()

	select {
	case client = <-cerrc:
	case <-ctx.Done():
		t.Fatal("client handshake never completed")
	}

	select {
	case server = <-serrc:
	case <-ctx.Done():
		t.Fatal("server handshake never completed")
	}

	return server, client
}

func TestHandshakeAcceptsMatchedPins(t *testing.T) {
	server, client := exchange(t, matching(t))

	require.NoError(t, server)
	require.NoError(t, client)
}

func TestHandshakeRejectsUnpinnedClient(t *testing.T) {
	e := matching(t)
	eve, _ := gencert(t)
	e.cliCert = &eve // a key the server was never told to accept

	server, _ := exchange(t, e)
	require.ErrorIs(t, server, ErrMismatch)
}

func TestHandshakeRejectsClientWithoutCertificate(t *testing.T) {
	e := matching(t)
	e.cliCert = nil

	// crypto/tls turns this away before Pinned runs, so it is not ErrMismatch
	server, _ := exchange(t, e)
	require.Error(t, server)
}

func TestHandshakeRejectsUnpinnedServer(t *testing.T) {
	e := matching(t)
	_, eve := gencert(t)
	e.cliPeer = eve // a key the server we reach does not hold

	_, client := exchange(t, e)
	require.ErrorIs(t, client, ErrMismatch)
}

func TestGRPCNeedsH2InNextProtos(t *testing.T) {
	for _, tt := range []struct {
		name       string
		nextProtos []string
		wantErr    bool
	}{
		{"with h2", []string{"h2"}, false},
		{"without", nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srvCert, srvFP := gencert(t)
			cliCert, cliFP := gencert(t)

			cfg := ServerTlsConfig(srvCert, cliFP)
			cfg.NextProtos = tt.nextProtos

			cconn, sconn := net.Pipe()
			defer cconn.Close()
			defer sconn.Close()

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			go func() {
				c := tls.Server(sconn, cfg)
				if err := c.HandshakeContext(ctx); err != nil {
					return
				}
				// keep reading to get the close_notify
				var b [1]byte
				c.Read(b[:])
			}()

			creds := credentials.NewTLS(ClientTlsConfig(&cliCert, srvFP))
			_, _, err := creds.ClientHandshake(ctx, "plakar-pod", cconn)

			if tt.wantErr {
				require.Error(t, err, "accepted conn without negotiated proto")
				require.Contains(t, err.Error(), "ALPN",
					"expected an ALPN error")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
