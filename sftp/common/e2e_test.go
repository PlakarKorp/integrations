package common

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestConnectSuccess(t *testing.T) {
	endpoint := &url.URL{
		Scheme: "sftp",
		Host:   fixture.host + ":" + fixture.port,
		Path:   "/",
		User:   url.User("testuser"),
	}

	// Each test is done on both openssh and native mode
	tests := []struct {
		name   string
		params map[string]string
	}{
		{
			name: "with identity",
			params: map[string]string{
				"identity":                 fixture.clientKeyPath,
				"insecure_ignore_host_key": "true",
			},
		},
		{
			name: "with ssh_private_key",
			params: map[string]string{
				"ssh_private_key":          fixture.clientKeyPEM,
				"insecure_ignore_host_key": "true",
			},
		},
	}
	modes := []string{"openssh", "native"}

	for _, tt := range tests {
		for _, mode := range modes {
			t.Run(mode+"-"+tt.name, func(t *testing.T) {
				params := cloneParams(tt.params)
				params["ssh_mode"] = mode
				if mode == "openssh" {
					t.Cleanup(func() {
						stopOpenSSHMaster(t, endpoint, params)
					})
				}

				client, err := Connect(endpoint, params)
				if err != nil {
					t.Fatalf("failed to connect: %v", err)
				}
				defer client.Close()

				if _, err := client.Getwd(); err != nil {
					t.Fatalf("sftp session unusable: %v", err)
				}
			})
		}
	}
}

func TestConnectFailure(t *testing.T) {

	endpoint := &url.URL{
		Scheme: "sftp",
		Host:   fixture.host + ":" + fixture.port,
		Path:   "/",
		User:   url.User("testuser"),
	}

	tests := []struct {
		name          string
		params        map[string]string
		errorContains []string
	}{
		{
			name: "native with missing identity",
			params: map[string]string{
				"ssh_mode":                 "native",
				"identity":                 filepath.Join(fixture.dir, "missing_identity"),
				"insecure_ignore_host_key": "true",
			},
			errorContains: []string{"failed to read identity file"},
		},
		{
			name: "native with invalid private key",
			params: map[string]string{
				"ssh_mode":                 "native",
				"ssh_private_key":          "invalid",
				"insecure_ignore_host_key": "true",
			},
			errorContains: []string{"failed to parse ssh_private_key", "no key found"},
		},
		{
			name: "openssh with invalid private key",
			params: map[string]string{
				"ssh_mode":                 "openssh",
				"ssh_private_key":          "invalid",
				"insecure_ignore_host_key": "true",
			},
			errorContains: []string{"Error loading key", "invalid format"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := Connect(endpoint, tt.params)
			if err == nil {
				if client != nil {
					client.Close()
				}
				t.Fatalf("expected error, got nil")
			}
			for _, errorContains := range tt.errorContains {
				if !strings.Contains(err.Error(), errorContains) {
					t.Fatalf("got error %q, want it to contain %q", err, errorContains)
				}
			}
			if client != nil {
				t.Fatalf("got %v, want nil", client)
			}
		})
	}
}

func cloneParams(params map[string]string) map[string]string {
	cloned := make(map[string]string, len(params)+1)
	for k, v := range params {
		cloned[k] = v
	}
	return cloned
}

func stopOpenSSHMaster(t *testing.T, endpoint *url.URL, params map[string]string) {
	t.Helper()

	sock, err := controlSock(endpoint, params)
	if err != nil {
		t.Logf("failed to resolve ssh control socket: %v", err)
		return
	}

	args := []string{"-S", sock, "-O", "exit"}
	if p := endpoint.Port(); p != "" {
		args = append(args, "-p", p)
	}
	if endpoint.User != nil {
		args = append(args, "-l", endpoint.User.Username())
	}
	args = append(args, endpoint.Hostname())

	_ = exec.Command("ssh", args...).Run()
}

// sftpFixture describes the shared, package-wide sftp server container.
type sftpFixture struct {
	containerID   string
	host          string // always 127.0.0.1
	port          string // mapped host port for container port 22
	clientKeyPEM  string // OpenSSH private key accepted by the server
	clientKeyPath string // temp identity file accepted by OpenSSH
	dir           string // temp dir containing fixture files
}

var fixture *sftpFixture

// TestMain sets up the sftp fixture and runs the tests, reusing the same fixture over multiple tests.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "docker not found in PATH; skipping docker-tagged tests")
		os.Exit(0)
	}

	f, err := startSFTPServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start sftp fixture: %v\n", err)
		os.Exit(1)
	}
	fixture = f

	code := m.Run()

	f.stop()
	os.Exit(code)
}

// startSFTPServer generates an ephemeral client key, boots one atmoz/sftp
// container, and waits until the SFTP subsystem is reachable.
func startSFTPServer() (*sftpFixture, error) {
	dir, err := os.MkdirTemp("", "sftp-fixture-")
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dir)
		}
	}()

	// Client key pair: private key is fed to connectNativeSSH, public key is
	// authorized on the server.
	clientPEM, clientAuthorized, err := genKeyPair()
	if err != nil {
		return nil, err
	}
	clientKeyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(clientKeyPath, []byte(clientPEM), 0600); err != nil {
		return nil, err
	}

	// atmoz/sftp copies every *.pub under /home/<user>/.ssh/keys into the
	// user's authorized_keys at startup.
	keysDir := filepath.Join(dir, "keys")
	if err := os.MkdirAll(keysDir, 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(keysDir, "client.pub"), []byte(clientAuthorized), 0644); err != nil {
		return nil, err
	}

	// Bind container port 22 to a random loopback port so parallel runs (and
	// whatever is already using :22) never collide.
	runOut, err := exec.Command("docker", "run", "-d",
		"-p", "127.0.0.1::22",
		"-v", keysDir+":/home/testuser/.ssh/keys:ro",
		"atmoz/sftp", "testuser::1001",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("docker run: %w", asExecErr(err))
	}
	containerID := strings.TrimSpace(string(runOut))

	f := &sftpFixture{
		containerID:   containerID,
		host:          "127.0.0.1",
		clientKeyPEM:  clientPEM,
		clientKeyPath: clientKeyPath,
		dir:           dir,
	}

	port, err := dockerMappedPort(containerID, "22")
	if err != nil {
		f.stop()
		return nil, err
	}
	f.port = port

	if err := waitForSSH(f, 60*time.Second); err != nil {
		f.stop()
		return nil, err
	}

	success = true
	return f, nil
}

func (f *sftpFixture) stop() {
	if f == nil {
		return
	}
	if f.containerID != "" {
		_ = exec.Command("docker", "rm", "-f", f.containerID).Run()
	}
	if f.dir != "" {
		_ = os.RemoveAll(f.dir)
	}
}

// genKeyPair returns an OpenSSH-format private key (PEM) and its matching
// authorized_keys line, using ed25519. No external tooling required.
func genKeyPair() (privPEM string, authorizedKey string, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", "", err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), string(ssh.MarshalAuthorizedKey(signer.PublicKey())), nil
}

// dockerMappedPort resolves the host port bound to the given container port.
func dockerMappedPort(containerID, containerPort string) (string, error) {
	out, err := exec.Command("docker", "port", containerID, containerPort).Output()
	if err != nil {
		return "", fmt.Errorf("docker port: %w", asExecErr(err))
	}
	// Output looks like "127.0.0.1:49153" (possibly multiple lines).
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	i := strings.LastIndex(line, ":")
	if i < 0 {
		return "", fmt.Errorf("unexpected docker port output: %q", string(out))
	}
	return line[i+1:], nil
}

// waitForSSH blocks until the server can serve a full SFTP session, not merely
// accept a TCP connection or complete an SSH handshake.
func waitForSSH(f *sftpFixture, timeout time.Duration) error {
	signer, err := ssh.ParsePrivateKey([]byte(f.clientKeyPEM))
	if err != nil {
		return err
	}
	cfg := &ssh.ClientConfig{
		User:            "testuser",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	}
	addr := f.host + ":" + f.port
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := trySFTP(addr, cfg); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for sftp at %s: %w", addr, lastErr)
}

// trySFTP performs one full SSH+SFTP handshake and tears it down.
func trySFTP(addr string, cfg *ssh.ClientConfig) error {
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	client, err := sftp.NewClient(conn)
	if err != nil {
		return err
	}
	defer client.Close()

	_, err = client.Getwd()
	return err
}

func asExecErr(err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}
