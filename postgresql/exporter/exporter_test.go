package exporter_test

import (
	"context"
	"io"
	"path"
	"strings"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/postgresql/exporter"
	"github.com/PlakarKorp/integrations/postgresql/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dump taken with format=directory is stored as a directory per database. Until
// restoring it is supported, each of its files must fail with a clear error
// instead of reaching pg_restore or psql. A custom-format dump of a database
// whose name holds a '/' also lands in a directory, and is not one of them.
func TestExportRejectsDirectoryFormatDumps(t *testing.T) {
	tests := []struct {
		pathname string
		rejected bool
	}{
		{"/00001-testdb/toc.dat", true},
		{"/00001-testdb/3001.dat", true},
		{"/00001-team/app.dump", false},
	}
	for _, tt := range tests {
		t.Run(tt.pathname, func(t *testing.T) {
			exp, err := exporter.NewExporterFromConfigMap(pgconn.ConnConfig{}, "", "postgresql",
				map[string]string{"pg_bin_dir": t.TempDir()}) // no pg_restore: never touch a server
			require.NoError(t, err)

			records := make(chan *integration.Record, 1)
			results := make(chan *integration.Result, 1)
			records <- integration.NewRecord(tt.pathname, "", integration.FileInfo{Lname: path.Base(tt.pathname), Lmode: 0444}, nil,
				func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil })
			close(records)

			require.NoError(t, exp.Export(context.Background(), nil, records, results))
			res := <-results
			if tt.rejected {
				assert.ErrorIs(t, res.Err, exporter.ErrDirectoryFormat)
			} else {
				assert.NotErrorIs(t, res.Err, exporter.ErrDirectoryFormat)
			}
		})
	}
}
