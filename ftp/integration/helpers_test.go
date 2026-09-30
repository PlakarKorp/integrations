package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PlakarKorp/integrations/ftp/conn"
	"github.com/secsy/goftp"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/exec"
)

var testDirs atomic.Int64

// testDir returns a directory name no other test, or other run of the same
// test under -count, uses on the shared server.
func testDir(t *testing.T) string {
	return fmt.Sprintf("/%s-%d", t.Name(), testDirs.Add(1))
}

func ftpConfig(addr, root string) map[string]string {
	return map[string]string{
		"location":               "ftp://" + addr + root,
		"username":               username,
		"password":               password,
		"tls_insecure_no_verify": "true",
	}
}

// client connects in plain FTP, for seeding and checking the server
// independently of the TLS path under test.  goftp also never completes the
// TLS handshake on an empty upload, so it cannot store empty files over TLS.
func client(t *testing.T, addr string) *goftp.Client {
	t.Helper()
	c, err := conn.ConnectToFTP(addr, conn.Options{
		Username:           username,
		Password:           password,
		TLS:                conn.TLSNone,
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// mkdirAll creates every missing component of dir.  goftp's Mkdir is a bare
// MKD, and the server refuses it when a parent is missing.
func mkdirAll(t *testing.T, c *goftp.Client, dir string) {
	t.Helper()
	cur := "/"
	for part := range strings.SplitSeq(strings.Trim(dir, "/"), "/") {
		cur = path.Join(cur, part)
		if _, err := c.Stat(cur); err == nil {
			continue
		}
		_, err := c.Mkdir(cur)
		require.NoError(t, err, "mkdir %s", cur)
	}
}

func put(t *testing.T, c *goftp.Client, name, content string) {
	t.Helper()
	mkdirAll(t, c, path.Dir(name))
	require.NoError(t, c.Store(name, strings.NewReader(content)), "store %s", name)
}

func get(t *testing.T, c *goftp.Client, name string) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, c.Retrieve(name, &buf), "retrieve %s", name)
	return buf.String()
}

func readAll(t *testing.T, rd io.ReadCloser) string {
	t.Helper()
	defer func() { _ = rd.Close() }()
	b, err := io.ReadAll(rd)
	require.NoError(t, err)
	return string(b)
}

// chmod changes the mode of name, relative to the FTP user's home, from
// inside the container.
func chmod(t *testing.T, name, mode string) {
	t.Helper()
	code, _, err := serverCtr.Exec(context.Background(), []string{"chmod", mode, "/home/ftpusers/" + username + name})
	require.NoError(t, err)
	require.Zero(t, code, "chmod %s %s", mode, name)
}

// sessions counts the sessions the server currently has open.  It takes a
// require.TestingT so that it can run under EventuallyWithT.
func sessions(t require.TestingT) int {
	code, out, err := serverCtr.Exec(context.Background(), []string{"pure-ftpwho", "-s"}, exec.Multiplexed())
	require.NoError(t, err)
	require.Zero(t, code, "pure-ftpwho")
	b, err := io.ReadAll(out)
	require.NoError(t, err)
	return strings.Count(string(b), "\n")
}
