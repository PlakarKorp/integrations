package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pg_dump takes the database name as its last argument. A name that libpq
// reads as a conninfo string, or pg_dump as an option, must be rejected before
// pg_dump runs.
func TestDumpDatabasesRejectsUnsafeNames(t *testing.T) {
	tests := []struct {
		name   string
		dbname string
	}{
		{"conninfo string redirects the connection", "host=127.0.0.1 port=15532 dbname=x"},
		{"leading dash is read as a pg_dump option", "-h127.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A fake pg_dump that records whether it was run.
			dir := t.TempDir()
			marker := filepath.Join(dir, "invoked")
			script := "#!/bin/sh\necho \"$@\" > " + marker + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "pg_dump"), []byte(script), 0o755))

			p := &Importer{pgBinDir: dir}
			records := make(chan *connectors.Record, 1)
			err := p.dumpDatabases(context.Background(), records, []string{tt.dbname})

			require.Error(t, err)
			assert.Empty(t, records, "record emitted for a rejected name")
			assert.NoFileExists(t, marker, "pg_dump was run with a rejected name")
		})
	}
}
