package routeros

import (
	"os"
	"path/filepath"
	"testing"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIL6qMBFF9C7Y5f3B6HQ0BPRHwKfMSNdaFcYBg5cLpBqL"

func TestHostKeyCallbackDefaultsToVerifying(t *testing.T) {
	// No known_hosts, no pin, no opt-out: refuse to connect rather than
	// accept whatever the device presents.
	missing := filepath.Join(t.TempDir(), "known_hosts")
	if _, err := hostKeyCallback(map[string]string{"known_hosts": missing}); err == nil {
		t.Fatal("a missing known_hosts was accepted")
	}
}

func TestHostKeyCallbackPinned(t *testing.T) {
	cb, err := hostKeyCallback(map[string]string{"host_key": testKey})
	if err != nil {
		t.Fatalf("host_key rejected: %v", err)
	}
	if cb == nil {
		t.Fatal("nil callback")
	}

	if _, err := hostKeyCallback(map[string]string{"host_key": "not a key"}); err == nil {
		t.Error("a malformed host_key was accepted")
	}
}

func TestHostKeyCallbackKnownHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte("router.example "+testKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := hostKeyCallback(map[string]string{"known_hosts": path}); err != nil {
		t.Fatalf("known_hosts rejected: %v", err)
	}
}

func TestHostKeyCallbackOptOut(t *testing.T) {
	cb, err := hostKeyCallback(map[string]string{"insecure_ignore_host_key": "true"})
	if err != nil || cb == nil {
		t.Fatalf("opt-out rejected: %v", err)
	}

	// false must not disable verification
	missing := filepath.Join(t.TempDir(), "known_hosts")
	if _, err := hostKeyCallback(map[string]string{
		"insecure_ignore_host_key": "false",
		"known_hosts":              missing,
	}); err == nil {
		t.Error("insecure_ignore_host_key=false skipped verification")
	}

	if _, err := hostKeyCallback(map[string]string{"insecure_ignore_host_key": "maybe"}); err == nil {
		t.Error("invalid insecure_ignore_host_key value accepted")
	}
}
