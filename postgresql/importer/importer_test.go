package importer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/integrations/postgresql/dumpfs"
	"github.com/PlakarKorp/integrations/postgresql/pgconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A single-database backup must not need CONNECT on "postgres".
func TestListDatabasesSingleDatabaseSkipsClusterEnumeration(t *testing.T) {
	p := &Importer{database: "appdb"}
	names, err := p.listDatabases(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"appdb"}, names)
}

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
			bin := fakePgDump(t, "")

			p := &Importer{pgBinDir: bin}
			records := make(chan *connectors.Record, 1)
			err := p.dumpDatabases(context.Background(), records, []string{tt.dbname})

			require.Error(t, err)
			assert.Empty(t, records, "record emitted for a rejected name")
			assert.NoFileExists(t, filepath.Join(bin, "args"), "pg_dump was run with a rejected name")
		})
	}
}

func TestFormatOptions(t *testing.T) {
	fuse := dumpfs.Available() == nil
	tests := []struct {
		name          string
		config        map[string]string
		concurrency   int // plakar's; 0 for no options
		wantErr       bool
		wantDirectory bool
		wantParallel  int
	}{
		{name: "default", config: map[string]string{}, wantParallel: 4},
		{name: "custom", config: map[string]string{"format": "custom"}, wantParallel: 4},
		{name: "unknown format", config: map[string]string{"format": "tar"}, wantErr: true},
		{name: "directory", config: map[string]string{"format": "directory"}, concurrency: 32, wantErr: !fuse, wantDirectory: true, wantParallel: 4},
		{name: "directory, low concurrency", config: map[string]string{"format": "directory"}, concurrency: 2, wantErr: !fuse, wantDirectory: true, wantParallel: 2},
		{name: "parallel_dumps set", config: map[string]string{"format": "directory", "parallel_dumps": "12"}, concurrency: 2, wantErr: !fuse, wantDirectory: true, wantParallel: 12},
		{name: "parallel_dumps zero", config: map[string]string{"format": "directory", "parallel_dumps": "0"}, wantErr: true},
		{name: "parallel_dumps not a number", config: map[string]string{"format": "directory", "parallel_dumps": "many"}, wantErr: true},
		{name: "parallel_dumps without directory", config: map[string]string{"parallel_dumps": "8"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts *connectors.Options
			if tt.concurrency > 0 {
				opts = &connectors.Options{MaxConcurrency: tt.concurrency}
			}
			p, err := NewImporterFromConfigMap(pgconn.ConnConfig{}, "", "", opts, tt.config)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantDirectory, p.directory)
			assert.Equal(t, tt.wantParallel, p.parallelDumps)
		})
	}
}

// fakePgDump installs a pg_dump that records its arguments in <bin>/args, then
// runs script with the -f directory in $dir.
func fakePgDump(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	body := "#!/bin/sh\necho \"$@\" > " + filepath.Join(bin, "args") + "\ndir=$2\n" + script
	require.NoError(t, os.WriteFile(filepath.Join(bin, "pg_dump"), []byte(body), 0o755))
	return bin
}

// readAll consumes records the way plakar does and returns their contents,
// or the read error in place of a file's content.
func readAll(records <-chan *connectors.Record) <-chan map[string]string {
	out := make(chan map[string]string, 1)
	go func() {
		files := map[string]string{}
		for rec := range records {
			data, err := io.ReadAll(rec.Reader)
			if err != nil {
				data = []byte("read error: " + err.Error())
			}
			files[rec.Pathname] = string(data)
		}
		out <- files
	}()
	return out
}

// exclude_tables and exclude_table_data reach pg_dump as one option per
// pattern, blank entries dropped, with "=" so a pattern cannot pass for an
// option.
func TestExcludeTablesOptions(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]string
		want   []string
		absent []string
	}{
		{
			name:   "unset",
			config: map[string]string{},
			absent: []string{"--exclude-table"},
		},
		{
			name: "tables and table data",
			config: map[string]string{
				"exclude_tables":     "public.audit, log_* ,",
				"exclude_table_data": "events",
			},
			want: []string{
				"--exclude-table=public.audit --exclude-table=log_* --exclude-table-data=events",
			},
		},
		{
			name:   "pattern starting with a dash",
			config: map[string]string{"exclude_tables": "-h127.0.0.1"},
			want:   []string{"--exclude-table=-h127.0.0.1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := fakePgDump(t, "")
			tt.config["pg_bin_dir"] = bin
			p, err := NewImporterFromConfigMap(pgconn.ConnConfig{}, "appdb", "", nil, tt.config)
			require.NoError(t, err)

			records := make(chan *connectors.Record)
			files := readAll(records)
			err = p.dumpDatabases(context.Background(), records, []string{"appdb"})
			close(records)
			<-files
			require.NoError(t, err)

			args, err := os.ReadFile(filepath.Join(bin, "args"))
			require.NoError(t, err)
			for _, w := range tt.want {
				assert.Contains(t, string(args), w)
			}
			for _, a := range tt.absent {
				assert.NotContains(t, string(args), a)
			}
		})
	}
}

// requireFUSE skips a test that mounts a dumpfs where FUSE is unavailable.
func requireFUSE(t *testing.T) {
	t.Helper()
	if err := dumpfs.Available(); err != nil {
		t.Skip(err)
	}
}

func TestParallelDumpEmitsDirectory(t *testing.T) {
	requireFUSE(t)
	bin := fakePgDump(t, `printf toc > "$dir/toc.dat"
printf rows > "$dir/3001.dat"
printf '16384 blob_16384.dat\n' >> "$dir/blobs_3.toc"
`)
	p := &Importer{database: "appdb", directory: true, parallelDumps: 4, pgBinDir: bin}
	records := make(chan *connectors.Record)
	files := readAll(records)

	err := p.dumpDatabases(context.Background(), records, []string{"appdb"})
	close(records)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"/00001-appdb/toc.dat":     "toc",
		"/00001-appdb/3001.dat":    "rows",
		"/00001-appdb/blobs_3.toc": "16384 blob_16384.dat\n",
	}, <-files)

	args, err := os.ReadFile(filepath.Join(bin, "args"))
	require.NoError(t, err)
	assert.Contains(t, string(args), "-Fd")
	assert.Contains(t, string(args), "-j 4 --no-sync appdb")
}

// A database pg_dump cannot dump fails the backup, single database or whole
// cluster: the files it handed over may be cut short.
func TestParallelDumpFailureFailsTheBackup(t *testing.T) {
	tests := []struct {
		name      string
		database  string
		databases []string
	}{
		{"single database", "appdb", []string{"appdb"}},
		{"cluster", "", []string{"bad", "good"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireFUSE(t)
			bin := fakePgDump(t, `printf part > "$dir/3001.dat"
echo 'connection lost' >&2
exit 1
`)
			p := &Importer{database: tt.database, directory: true, parallelDumps: 4, pgBinDir: bin}
			records := make(chan *connectors.Record)
			files := readAll(records)

			err := p.dumpDatabases(context.Background(), records, tt.databases)
			close(records)
			got := <-files
			assert.ErrorContains(t, err, "connection lost")
			assert.NotContains(t, got, "/00002-good/3001.dat", "dumped the next database")
		})
	}
}

// Cancelling the backup kills the pg_dump leader only. Its workers, blocked
// writing files nobody reads, must be released or the dump never returns.
func TestParallelDumpStopsOnCancel(t *testing.T) {
	requireFUSE(t)
	// More than a piece, so the write blocks until something reads it.
	bin := fakePgDump(t, `head -c 3000000 /dev/zero > "$dir/3001.dat" &
wait
`)
	p := &Importer{database: "appdb", directory: true, parallelDumps: 4, pgBinDir: bin}
	ctx, cancel := context.WithCancel(context.Background())
	records := make(chan *connectors.Record)
	go func() {
		<-records // take the record, never read it
		cancel()
		for range records {
		}
	}()

	err := p.dumpDatabases(ctx, records, []string{"appdb"})
	close(records)
	assert.Error(t, err)
}

// An empty table gives an empty file. Its record must still differ from the
// previous snapshot's, or kloset reuses that data and never reads the stream.
func TestParallelDumpRecordsAreNeverCached(t *testing.T) {
	requireFUSE(t)
	bin := fakePgDump(t, `: > "$dir/3002.dat"
`)
	p := &Importer{database: "appdb", directory: true, parallelDumps: 4, pgBinDir: bin}
	records := make(chan *connectors.Record)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for rec := range records {
			assert.False(t, rec.FileInfo.ModTime().IsZero(), rec.Pathname)
			_, _ = io.ReadAll(rec.Reader)
		}
	}()

	err := p.dumpDatabases(context.Background(), records, []string{"appdb"})
	close(records)
	<-done
	require.NoError(t, err)
}
