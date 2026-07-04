package common

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

var testSSHCert = `
-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACCeF/EJkMysMrz2pWZfs95OtIqbDkz5jJHdXEI2aQ+ZQAAAALBS9q/GUvav
xgAAAAtzc2gtZWQyNTUxOQAAACCeF/EJkMysMrz2pWZfs95OtIqbDkz5jJHdXEI2aQ+ZQA
AAAEChwRRqrpme6kwm/PVrr7AmODBU2ZpcMy0eLmOJn6EdpJ4X8QmQzKwyvPalZl+z3k60
ipsOTPmMkd1cQjZpD5lAAAAAKXBhdWxvb3N0ZW5yaWprQE1hY0Jvb2stUHJvLXZhbi1QYX
VsLmxvY2FsAQIDBA==
-----END OPENSSH PRIVATE KEY-----
`

var testKnownHosts = `
127.0.0.1 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID3ZhoyDd985pxjRly55oSAWdvNQEBJFMWceKsIcFpzV
127.0.0.1 ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC0MBAGzc743Qro2yFD/aAQRiw6w4BFzptGxvKSZbJ0oicmwcL756yxIzPyVNC3rM3vpJzq98UDcBuc4KfK3IHwvQqmahbPCJtZJHZzNbQhfHxFDuZSRONTq3c0PUcYlVGGQ56KcAXM4C/ik4MNI/SSNZjh1hxNYzhMp2rlxyUYKA+79Sm3oMXOjd099hu96NeRbV8uhePLT0EV+mS853JLWAXAAvqfNCnOFHeRHuJGUYPsl897PHUsRfKU2bFBztHyTX+s6L+B9l6Dq3wi7cDfczAJPCzT5Ryl6G7Ywp5lEJFn1dXio1cqihdA/iag+IwLXC5hjNWOSczjRn94jtstiXP8uVbluzI2qR/BQ5tHFQcP4F5V/qTgwl0DsMzmkPreBFG6yBMQc5Zth/RdNmX5yVPY0UG2631IVeDLrO9Ep0wbk13f4hcy8wCDBF8f3o7XQZrzw6Db/D+NZL1p4toCCkpxL5FRBWAXB0MIJ1oMbiMLLuM/2At3tTKZ7Lq7eF8=
127.0.0.1 ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBOSjgoFyp8nQBRXL7Y/6nl2HyA59BHMrwMrlIrhBaQ/DTN8Z5SX0WNyTsCnYfatYMh7Vnsf5bF3lSfKnxvlD6dc=
`

func TestBuildAuthMethodsSuccess(t *testing.T) {
	got, err := buildAuthMethods(map[string]string{
		"ssh_private_key": testSSHCert,
	})
	if err != nil {
		t.Fatalf("failed to build auth methods: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d auth methods, want 1", len(got))
	}
}

func TestBuildAuthMethodsFailure(t *testing.T) {
	got, err := buildAuthMethods(map[string]string{
		"ssh_private_key": "invalid",
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestHostKeyCallbackFailureInvalidKnownHosts(t *testing.T) {
	testDir := t.TempDir()
	os.WriteFile(filepath.Join(testDir, "known_hosts"), []byte("invalid"), 0644)
	got, err := hostKeyCallback(map[string]string{
		"known_hosts": filepath.Join(testDir, "known_hosts"),
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestHostKeyCallbackSuccessValid(t *testing.T) {
	testDir := t.TempDir()
	os.WriteFile(filepath.Join(testDir, "known_hosts"), []byte(testKnownHosts), 0644)
	got, err := hostKeyCallback(map[string]string{
		"known_hosts": filepath.Join(testDir, "known_hosts"),
	})
	if err != nil {
		t.Fatalf("failed to build host key callback: %v", err)
	}
	if got == nil {
		t.Fatalf("got nil, want non-nil")
	}
}

func TestConnectNativeSSHWithoutUsernameFailure(t *testing.T) {
	endpoint := &url.URL{
		Scheme: "sftp",
		Host:   "localhost",
		Path:   "/",
		User:   nil,
	}
	client, err := connectNativeSSH(endpoint, map[string]string{
		"ssh_mode": "native",
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if client != nil {
		t.Fatalf("got %v, want nil", client)
	}
}
