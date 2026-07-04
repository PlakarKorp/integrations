package common

import (
	"net/url"
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
