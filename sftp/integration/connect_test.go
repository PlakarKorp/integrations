package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/sftp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestConnectReadsServerFiles(t *testing.T) {
	s := getServer(t)

	dir := path.Join(home, t.Name())
	_, err := run(context.Background(), s.ctr, "sh", "-c", `mkdir -p "$1" && printf hello > "$1/greeting"`, "sh", dir)
	require.NoError(t, err)

	endpoint := &url.URL{Scheme: "sftp", Host: s.host + ":" + s.port, Path: dir}
	client, err := sftp.Connect(endpoint, s.params())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	entries, err := client.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "greeting", entries[0].Name())

	f, err := client.Open(path.Join(dir, "greeting"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "hello", string(content))
}

// freshIdentity returns a copy of the server's key at a new path.  Connect
// keys its ControlMaster on the identity among others, so this forces a new
// ssh handshake instead of reusing another test's master.
func freshIdentity(t *testing.T, s *sshServer) string {
	t.Helper()
	key, err := os.ReadFile(s.identity)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "id")
	require.NoError(t, os.WriteFile(p, key, 0o600))
	return p
}

func (s *sshServer) endpoint() *url.URL {
	return &url.URL{Scheme: "sftp", Host: s.host + ":" + s.port}
}

func connect(t *testing.T, u *url.URL, params map[string]string) error {
	t.Helper()
	client, err := sftp.Connect(u, params)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	_, err = client.Getwd()
	return err
}

func TestConnectRefusesWrongHostKey(t *testing.T) {
	s := getServer(t)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	other, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)

	params := s.params()
	params["identity"] = freshIdentity(t, s)
	params["host_key"] = fmt.Sprintf("[%s]:%s %s", s.host, s.port, ssh.MarshalAuthorizedKey(other))
	require.ErrorContains(t, connect(t, s.endpoint(), params), "Host key verification failed")
}

func TestConnectWithPrivateKeyThroughAgent(t *testing.T) {
	s := getServer(t)

	sock := filepath.Join(runtimeDir, fmt.Sprintf("agent-%d.sock", testDirs.Add(1)))
	agent := exec.Command("ssh-agent", "-D", "-a", sock)
	require.NoError(t, agent.Start())
	t.Cleanup(func() {
		_ = agent.Process.Kill()
		_ = agent.Wait()
	})
	require.Eventually(t, func() bool {
		_, err := os.Stat(sock)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)

	key, err := os.ReadFile(s.identity)
	require.NoError(t, err)

	params := s.params()
	delete(params, "identity")
	params["ssh_private_key"] = string(key)
	params["ssh_auth_sock"] = sock
	require.NoError(t, connect(t, s.endpoint(), params))
}

func TestConnectUserInLocation(t *testing.T) {
	s := getServer(t)

	u := s.endpoint()
	u.User = url.User(username)
	params := s.params()
	delete(params, "username")
	require.NoError(t, connect(t, u, params))
}

func TestUserInLocationAndUsernameConflict(t *testing.T) {
	s := getServer(t)

	cfg := s.config(home)
	cfg["location"] = fmt.Sprintf("sftp://%s@%s:%s%s", username, s.host, s.port, home)
	_, err := sftp.NewImporter(t.Context(), options(), "sftp", cfg)
	require.ErrorContains(t, err, "cannot use user@host syntax and username parameter")
}

func TestConnectWrongUserRefused(t *testing.T) {
	s := getServer(t)

	params := s.params()
	params["username"] = "nobody"
	require.ErrorContains(t, connect(t, s.endpoint(), params), "Permission denied")
}
