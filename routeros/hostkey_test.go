package routeros

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIL6qMBFF9C7Y5f3B6HQ0BPRHwKfMSNdaFcYBg5cLpBqL"

func newWithHostKeyConfig(t *testing.T, extra map[string]string) error {
	t.Helper()
	config := map[string]string{
		"location": "routeros+export://user@host",
		"password": "secret",
	}
	for k, v := range extra {
		config[k] = v
	}
	_, err := NewImporter(context.Background(), nil, "routeros+export", config)
	return err
}

func TestHostKeyDefaultsToUserKnownHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := newWithHostKeyConfig(t, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(home, ".ssh", "known_hosts"))
}

func TestHostKeyKnownHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	require.Error(t, newWithHostKeyConfig(t, map[string]string{"known_hosts_file": path}))

	require.NoError(t, os.WriteFile(path, []byte("host "+testHostKey+"\n"), 0600))
	assert.NoError(t, newWithHostKeyConfig(t, map[string]string{"known_hosts_file": path}))
}

func TestHostKeyInsecureIgnore(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "known_hosts")

	assert.NoError(t, newWithHostKeyConfig(t, map[string]string{
		"insecure_ignore_host_key": "true",
		"known_hosts_file":         missing,
	}))
	assert.Error(t, newWithHostKeyConfig(t, map[string]string{
		"insecure_ignore_host_key": "false",
		"known_hosts_file":         missing,
	}), "false must not disable verification")
	assert.Error(t, newWithHostKeyConfig(t, map[string]string{
		"insecure_ignore_host_key": "maybe",
	}))
}

func TestHostKeyPinned(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "known_hosts")

	assert.NoError(t, newWithHostKeyConfig(t, map[string]string{
		"host_key":         testHostKey,
		"known_hosts_file": missing,
	}), "a pinned key must not need known_hosts")
	assert.Error(t, newWithHostKeyConfig(t, map[string]string{
		"host_key": "not a key",
	}))
}
