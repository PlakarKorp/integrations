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
	"github.com/stretchr/testify/assert"
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
				"identity":    fixture.clientKeyPath,
				"known_hosts": fixture.knownHostsPath(),
			},
		},
		{
			name: "with ssh_private_key",
			params: map[string]string{
				"ssh_private_key": fixture.clientKeyPEM,
				"known_hosts":     fixture.knownHostsPath(),
			},
		},
		{
			name: "without known_hosts skip verification",
			params: map[string]string{
				"identity":                 fixture.clientKeyPath,
				"insecure_ignore_host_key": "true",
			},
		},
		{
			name: "with identity and known_hosts",
			params: map[string]string{
				"identity":    fixture.clientKeyPath,
				"known_hosts": fixture.knownHostsPath(),
			},
		},
		{
			name: "with ssh_private_key and known_hosts",
			params: map[string]string{
				"ssh_private_key": fixture.clientKeyPEM,
				"known_hosts":     fixture.knownHostsPath(),
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
				"ssh_mode":    "native",
				"identity":    filepath.Join(fixture.knownHostsDir, "missing_identity"),
				"known_hosts": fixture.knownHostsPath(),
			},
			errorContains: []string{"failed to read identity file"},
		},
		{
			name: "native with invalid known_hosts",
			params: map[string]string{
				"ssh_mode":        "native",
				"ssh_private_key": fixture.clientKeyPEM,
				"known_hosts":     fixture.unknownHostKeys,
			},
			errorContains: []string{"key is unknown"},
		},
		{
			name: "openssh with invalid known_hosts",
			params: map[string]string{
				"ssh_mode":        "openssh",
				"ssh_private_key": fixture.clientKeyPEM,
				"known_hosts":     fixture.unknownHostKeys,
			},
			errorContains: []string{"Host key verification failed."},
		},
		{
			name: "native with invalid private key",
			params: map[string]string{
				"ssh_mode":        "native",
				"ssh_private_key": "invalid",
				"known_hosts":     fixture.knownHostsPath(),
			},
			errorContains: []string{"failed to parse ssh_private_key", "no key found"},
		},
		{
			name: "openssh with invalid private key",
			params: map[string]string{
				"ssh_mode":        "openssh",
				"ssh_private_key": "invalid",
				"known_hosts":     fixture.knownHostsPath(),
			},
			errorContains: []string{"Error loading key", "invalid format"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := Connect(endpoint, tt.params)
			for _, errorContains := range tt.errorContains {
				assert.ErrorContains(t, err, errorContains, tt.name)
			}
			assert.Nil(t, client)
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
	containerID     string
	host            string // always 127.0.0.1
	port            string // mapped host port for container port 22
	clientKeyPEM    string // OpenSSH private key accepted by the server
	clientKeyPath   string // temp identity file accepted by OpenSSH
	knownHosts      string // known_hosts content matching the server's host key + port
	unknownHostKeys string // unknown host keys content
	knownHostsDir   string // temp dir containing a ready-to-use "known_hosts" file
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
		knownHostsDir: dir,
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

	// Build known_hosts from the server's *actual* host keys (all algorithms it
	// serves), so verification succeeds regardless of which host-key algorithm
	// the Go client negotiates.
	if err := f.writeKnownHostsFromContainer(); err != nil {
		f.stop()
		return nil, err
	}

	fakeHostKeys := `
	127.0.0.1 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID3ZhoyDd985pxjRly55oSAWdvNQEBJFMWceKsIcFpzV
	127.0.0.1 ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC0MBAGzc743Qro2yFD/aAQRiw6w4BFzptGxvKSZbJ0oicmwcL756yxIzPyVNC3rM3vpJzq98UDcBuc4KfK3IHwvQqmahbPCJtZJHZzNbQhfHxFDuZSRONTq3c0PUcYlVGGQ56KcAXM4C/ik4MNI/SSNZjh1hxNYzhMp2rlxyUYKA+79Sm3oMXOjd099hu96NeRbV8uhePLT0EV+mS853JLWAXAAvqfNCnOFHeRHuJGUYPsl897PHUsRfKU2bFBztHyTX+s6L+B9l6Dq3wi7cDfczAJPCzT5Ryl6G7Ywp5lEJFn1dXio1cqihdA/iag+IwLXC5hjNWOSczjRn94jtstiXP8uVbluzI2qR/BQ5tHFQcP4F5V/qTgwl0DsMzmkPreBFG6yBMQc5Zth/RdNmX5yVPY0UG2631IVeDLrO9Ep0wbk13f4hcy8wCDBF8f3o7XQZrzw6Db/D+NZL1p4toCCkpxL5FRBWAXB0MIJ1oMbiMLLuM/2At3tTKZ7Lq7eF8=
	127.0.0.1 ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBOSjgoFyp8nQBRXL7Y/6nl2HyA59BHMrwMrlIrhBaQ/DTN8Z5SX0WNyTsCnYfatYMh7Vnsf5bF3lSfKnxvlD6dc=
	`
	// Write fake host keys to a file
	unknownHostKeysPath := filepath.Join(f.knownHostsDir, "unknown_host_keys")
	if err := os.WriteFile(unknownHostKeysPath, []byte(fakeHostKeys), 0644); err != nil {
		return nil, fmt.Errorf("failed to write host keys: %w", err)
	}
	f.unknownHostKeys = unknownHostKeysPath

	success = true
	return f, nil
}

// writeKnownHostsFromContainer scrapes the server's public host keys and writes a
// known_hosts file entry (in "[host]:port" form for the mapped port) for each.
func (f *sftpFixture) writeKnownHostsFromContainer() error {
	out, err := exec.Command("docker", "exec", f.containerID,
		"sh", "-c", "cat /etc/ssh/ssh_host_*_key.pub").Output()
	if err != nil {
		return fmt.Errorf("read host keys: %w", asExecErr(err))
	}

	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(l)
		if len(fields) < 2 {
			continue
		}
		// "[host]:port keytype base64" — drop any trailing comment field.
		lines = append(lines, fmt.Sprintf("[%s]:%s %s %s", f.host, f.port, fields[0], fields[1]))
	}
	if len(lines) == 0 {
		return fmt.Errorf("no host keys found on server")
	}

	f.knownHosts = strings.Join(lines, "\n") + "\n"
	return os.WriteFile(f.knownHostsPath(), []byte(f.knownHosts), 0644)
}

func (f *sftpFixture) knownHostsPath() string {
	return filepath.Join(f.knownHostsDir, "known_hosts")
}

func (f *sftpFixture) stop() {
	if f == nil {
		return
	}
	if f.containerID != "" {
		_ = exec.Command("docker", "rm", "-f", f.containerID).Run()
	}
	if f.knownHostsDir != "" {
		_ = os.RemoveAll(f.knownHostsDir)
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
