package pgconn_test

import (
	"context"
	"maps"
	"os"
	"testing"

	"github.com/PlakarKorp/integrations/postgresql/awsexporter"
	"github.com/PlakarKorp/integrations/postgresql/awsimporter"
	"github.com/PlakarKorp/integrations/postgresql/binimporter"
	"github.com/PlakarKorp/integrations/postgresql/exporter"
	"github.com/PlakarKorp/integrations/postgresql/importer"
	"github.com/PlakarKorp/integrations/postgresql/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParseConnConfig only copies ssl_key_data to a temp file, it never parses it.
const testSSLKey = "-----BEGIN PRIVATE KEY-----"

func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Every constructor writes ssl_key_data to a temp file first, then validates
// the rest of its config. Each case passes a key plus one option that fails
// that later validation: the constructor must fail and leave no key behind.
func TestConstructorErrorRemovesTempSSLKey(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		badOpt  map[string]string // rejected after the key file is written
		wantErr string
		newConn func(config map[string]string) error
	}{
		{
			name:    "importer: compress is not a boolean",
			badOpt:  map[string]string{"compress": "bogus"},
			wantErr: `compress: strconv.ParseBool: parsing "bogus": invalid syntax`,
			newConn: func(c map[string]string) error {
				_, err := importer.NewImporter(ctx, nil, "", c)
				return err
			},
		},
		{
			name:    "binimporter: location has a database subpath",
			badOpt:  map[string]string{"location": "postgres+bin://localhost/mydb"},
			wantErr: `postgres+bin: subpath "mydb" is not allowed`,
			newConn: func(c map[string]string) error {
				_, err := binimporter.NewBinImporter(ctx, nil, "", c)
				return err
			},
		},
		{
			name:    "exporter: clean is not a boolean",
			badOpt:  map[string]string{"clean": "bogus"},
			wantErr: `clean: strconv.ParseBool: parsing "bogus": invalid syntax`,
			newConn: func(c map[string]string) error {
				_, err := exporter.NewExporter(ctx, nil, "", c)
				return err
			},
		},
		{
			name:    "awsimporter: region is missing",
			badOpt:  map[string]string{"region": ""},
			wantErr: "postgres+aws: region is required",
			newConn: func(c map[string]string) error {
				_, err := awsimporter.NewAWSImporter(ctx, nil, "", c)
				return err
			},
		},
		{
			name:    "awsexporter: region is missing",
			badOpt:  map[string]string{"region": ""},
			wantErr: "postgres+aws: region is required",
			newConn: func(c map[string]string) error {
				_, err := awsexporter.NewAWSExporter(ctx, nil, "", c)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)

			config := map[string]string{"ssl_key_data": testSSLKey}
			maps.Copy(config, tt.badOpt)

			require.ErrorContains(t, tt.newConn(config), tt.wantErr)
			assert.Empty(t, tempFiles(t, tmp), "temp SSL key left on disk")
		})
	}
}

// On success the key must stay on disk until Cleanup, which Close calls.
func TestParseConnConfigKeepsTempSSLKeyUntilCleanup(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	conn, _, err := pgconn.ParseConnConfig(map[string]string{"ssl_key_data": testSSLKey})
	require.NoError(t, err)
	require.FileExists(t, conn.SSLKey)

	conn.Cleanup()
	assert.NoFileExists(t, conn.SSLKey)
}
