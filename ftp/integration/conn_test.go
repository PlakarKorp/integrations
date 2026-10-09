package integration

import (
	"testing"

	"github.com/PlakarKorp/integrations/ftp/conn"
	"github.com/stretchr/testify/require"
)

// goftp dials lazily, so each test issues a command to force the handshake.

func TestTLSVerifiesCertificateByDefault(t *testing.T) {
	t.Parallel()
	addr := server(t)

	opts, err := conn.ParseOptions(map[string]string{"username": username, "password": password})
	require.NoError(t, err)
	c, err := conn.ConnectToFTP(addr, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// goftp flattens the error, so tls.CertificateVerificationError is lost.
	_, err = c.ReadDir("/")
	require.ErrorContains(t, err, "failed to verify certificate")
}

func TestExplicitTLS(t *testing.T) {
	t.Parallel()
	addr := server(t)

	opts, err := conn.ParseOptions(map[string]string{
		"username":               username,
		"password":               password,
		"tls_insecure_no_verify": "true",
	})
	require.NoError(t, err)
	c, err := conn.ConnectToFTP(addr, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.ReadDir("/")
	require.NoError(t, err)
}

func TestPlainFTPWhenAcknowledged(t *testing.T) {
	t.Parallel()
	addr := server(t)

	opts, err := conn.ParseOptions(map[string]string{
		"username":               username,
		"password":               password,
		"tls":                    "none",
		"tls_insecure_no_verify": "true",
	})
	require.NoError(t, err)
	c, err := conn.ConnectToFTP(addr, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.ReadDir("/")
	require.NoError(t, err)
}

func TestChmod(t *testing.T) {
	t.Parallel()
	addr := server(t)
	c := client(t, addr)
	dir := testDir(t)
	put(t, c, dir+"/a.txt", "alpha")

	require.NoError(t, c.Chmod(dir+"/a.txt", 0o600))
	require.Equal(t, "600", mode(t, dir+"/a.txt"))
}

func TestChmodReportsRefusal(t *testing.T) {
	t.Parallel()
	addr := server(t)
	c := client(t, addr)

	require.Error(t, c.Chmod(testDir(t)+"/missing", 0o600))
}
