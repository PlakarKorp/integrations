// Package integration runs the connector against a real OpenSSH server in a
// container, through the system ssh client.
package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
)

const (
	image    = "lscr.io/linuxserver/openssh-server:10.3_p1-r2-ls243@sha256:161e0b670b31d9539ca1718bc5943e981779331b4e1c9d32e8929d7652a1f22f"
	username = "plakar"
	home     = "/config"
)

type sshServer struct {
	ctr      testcontainers.Container
	host     string
	port     string
	hostKey  string // known_hosts line for host:port
	identity string // private key file
}

// params returns the connector configuration for this server.
func (s *sshServer) params() map[string]string {
	return map[string]string{
		"username": username,
		"identity": s.identity,
		"host_key": s.hostKey,
	}
}

var (
	// Connect keeps its ControlMaster sockets under $XDG_RUNTIME_DIR.  We
	// point it at our own directory to find and stop the masters on exit,
	// as ControlPersist would keep them running for ten minutes.  Unix
	// socket paths are limited to ~108 bytes, too short for t.TempDir().
	runtimeDir string

	serverOnce sync.Once
	server     *sshServer
	errServer  error
)

func TestMain(m *testing.M) {
	var err error
	runtimeDir, err = os.MkdirTemp("", "sftp-it")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	// ssh offers agent keys before -i; a developer's agent with many keys
	// exhausts MaxAuthTries before ours is tried.
	_ = os.Unsetenv("SSH_AUTH_SOCK")

	code := m.Run()

	stopMasters()
	if server != nil && server.ctr != nil {
		_ = server.ctr.Terminate(context.Background(), testcontainers.StopTimeout(0))
	}
	_ = os.RemoveAll(runtimeDir)
	os.Exit(code)
}

func stopMasters() {
	socks, _ := filepath.Glob(filepath.Join(runtimeDir, "plakar", "ssh", "*.sock"))
	for _, sock := range socks {
		_ = exec.Command("ssh", "-S", sock, "-O", "exit", "unused").Run()
	}
}

// getServer returns the shared server, starting it on first use.
func getServer(t *testing.T) *sshServer {
	t.Helper()
	if testing.Short() {
		t.Skip("needs docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	serverOnce.Do(func() {
		server, errServer = startServer(context.Background())
	})
	require.NoError(t, errServer)
	return server
}

func startServer(ctx context.Context) (*sshServer, error) {
	identity, pubKey, err := generateKey()
	if err != nil {
		return nil, err
	}

	req := testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: []string{"2222/tcp"},
		Env: map[string]string{
			"USER_NAME":  username,
			"PUBLIC_KEY": pubKey,
		},
		WaitingFor: wait.ForAll(
			wait.ForLog("[ls.io-init] done."),
			wait.ForListeningPort("2222/tcp"),
		),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	s := &sshServer{ctr: ctr, identity: identity}
	if err != nil {
		return s, err
	}

	s.host, err = ctr.Host(ctx)
	if err != nil {
		return s, err
	}
	port, err := ctr.MappedPort(ctx, "2222/tcp")
	if err != nil {
		return s, err
	}
	s.port = port.Port()

	// Read the key from the container rather than scanning it over the
	// network, so the host key check is a real one.
	key, err := run(ctx, ctr, "cat", "/config/ssh_host_keys/ssh_host_ed25519_key.pub")
	if err != nil {
		return s, err
	}
	fields := strings.Fields(key)
	if len(fields) < 2 {
		return s, fmt.Errorf("unexpected host key %q", key)
	}
	// ssh looks up non-standard ports as [host]:port.
	s.hostKey = fmt.Sprintf("[%s]:%s %s %s", s.host, s.port, fields[0], fields[1])

	return s, nil
}

// generateKey writes a fresh ed25519 private key under runtimeDir and returns
// its path and the authorized_keys line for it.
func generateKey() (string, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(runtimeDir, "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return path, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

// run executes cmd in the container as the ssh user and returns its stdout.
func run(ctx context.Context, ctr testcontainers.Container, cmd ...string) (string, error) {
	code, rd, err := ctr.Exec(ctx, append([]string{"s6-setuidgid", username}, cmd...), tcexec.Multiplexed())
	if err != nil {
		return "", err
	}
	out, err := io.ReadAll(rd)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("%q exited %d: %s", cmd, code, out)
	}
	return string(out), nil
}
