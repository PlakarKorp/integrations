package mysqlconn

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConnConfig_SSLDataParams(t *testing.T) {
	t.Run("path only", func(t *testing.T) {
		cc, err := ParseConnConfig(false, map[string]string{"ssl_cert": "/etc/ssl/client.pem"})
		require.NoError(t, err)
		assert.Equal(t, "/etc/ssl/client.pem", cc.SSLCert)
		assert.Empty(t, cc.tmpFiles)
	})

	t.Run("inline data writes a temp file", func(t *testing.T) {
		cc, err := ParseConnConfig(false, map[string]string{"ssl_cert_data": "fake-pem-content"})
		require.NoError(t, err)
		require.Len(t, cc.tmpFiles, 1)

		content, err := os.ReadFile(cc.SSLCert)
		require.NoError(t, err)
		assert.Equal(t, "fake-pem-content", string(content))

		cc.Cleanup()
		_, err = os.Stat(cc.SSLCert)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("path and data together are rejected", func(t *testing.T) {
		_, err := ParseConnConfig(false, map[string]string{
			"ssl_key":      "/etc/ssl/client-key.pem",
			"ssl_key_data": "fake-key-content",
		})
		require.ErrorContains(t, err, `"ssl_key" and "ssl_key_data" are mutually exclusive`)
	})

	t.Run("all three file params support inline data", func(t *testing.T) {
		cc, err := ParseConnConfig(false, map[string]string{
			"ssl_cert_data": "cert",
			"ssl_key_data":  "key",
			"ssl_ca_data":   "ca",
		})
		require.NoError(t, err)
		defer cc.Cleanup()

		for _, path := range []string{cc.SSLCert, cc.SSLKey, cc.SSLCA} {
			require.FileExists(t, path)
		}
		assert.Len(t, cc.tmpFiles, 3)
	})
}
