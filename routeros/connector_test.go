package routeros

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestNewRedactsCredentials(t *testing.T) {
	loc := "routeros+export://user:secret@host/%zz"
	_, err := NewImporter(context.Background(), nil, "routeros+export",
		map[string]string{"location": loc})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")
}

func TestNewPrivateKeyPassphrase(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	require.NoError(t, err)

	key := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(key, pem.EncodeToMemory(block), 0600))

	newImporter := func(pass string) error {
		_, err := NewImporter(context.Background(), nil, "routeros+export",
			map[string]string{
				"location":                 "routeros+export://user@host",
				"private_key":              key,
				"private_key_passphrase":   pass,
				"insecure_ignore_host_key": "true",
			})
		return err
	}

	assert.NoError(t, newImporter("hunter2"))
	assert.Error(t, newImporter("wrong"))
}
