package integration

import (
	"io"
	"io/fs"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/ftp/exporter"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dirRecord(pathname string) *connectors.Record {
	return dirRecordMode(pathname, 0o755)
}

func dirRecordMode(pathname string, perm fs.FileMode) *connectors.Record {
	return connectors.NewRecord(pathname, "", objects.FileInfo{Lmode: fs.ModeDir | perm, Lnlink: 1}, nil, nil)
}

func fileRecord(pathname, content string) *connectors.Record {
	return fileRecordMode(pathname, content, 0o644)
}

func fileRecordMode(pathname, content string, perm fs.FileMode) *connectors.Record {
	fi := objects.FileInfo{Lsize: int64(len(content)), Lmode: perm, Lnlink: 1}
	return connectors.NewRecord(pathname, "", fi, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	})
}

// export feeds records to a fresh exporter rooted at root and returns the
// per-record errors, keyed by pathname.
func export(t *testing.T, addr, root string, recs ...*connectors.Record) map[string]error {
	t.Helper()
	ctx := t.Context()

	exp, err := exporter.NewExporter(ctx, &connectors.Options{MaxConcurrency: 4}, "ftp", ftpConfig(addr, root))
	require.NoError(t, err)
	defer func() { _ = exp.Close(ctx) }()

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	errc := make(chan error, 1)
	go func() { errc <- exp.Export(ctx, records, results) }()
	go func() {
		defer close(records)
		for _, r := range recs {
			records <- r
		}
	}()

	errs := map[string]error{}
	for res := range results {
		errs[res.Record.Pathname] = res.Err
	}
	require.NoError(t, <-errc)
	return errs
}

func TestExportWritesTree(t *testing.T) {
	t.Parallel()
	addr := server(t)
	c := client(t, addr)
	dir := testDir(t)
	mkdirAll(t, c, dir)

	errs := export(t, addr, dir,
		dirRecord("/"),
		dirRecord("/sub"),
		fileRecord("/a.txt", "alpha"),
		fileRecord("/sub/b.txt", "bravo"),
	)
	for name, err := range errs {
		assert.NoError(t, err, name)
	}

	assert.Equal(t, "alpha", get(t, c, dir+"/a.txt"))
	assert.Equal(t, "bravo", get(t, c, dir+"/sub/b.txt"))

	for _, d := range []string{dir, dir + "/sub"} {
		entries, err := c.ReadDir(d)
		require.NoError(t, err)
		for _, e := range entries {
			assert.NotContains(t, e.Name(), ".tmp.", "temporary file left behind in %s", d)
		}
	}
}

func TestExportRestoresPermissions(t *testing.T) {
	t.Parallel()
	addr := server(t)
	c := client(t, addr)
	dir := testDir(t)
	mkdirAll(t, c, dir)

	// A read-only directory must still receive its children, and one that
	// cannot be traversed must not stop its children's modes being set.
	errs := export(t, addr, dir,
		dirRecord("/"),
		dirRecordMode("/ro", 0o555),
		fileRecordMode("/ro/secret.txt", "s3cr3t", 0o600),
		dirRecordMode("/nox", 0o600),
		dirRecordMode("/nox/sub", 0o700),
		fileRecordMode("/a.txt", "alpha", 0o640),
	)
	for name, err := range errs {
		assert.NoError(t, err, name)
	}

	assert.Equal(t, "555", mode(t, dir+"/ro"))
	assert.Equal(t, "600", mode(t, dir+"/ro/secret.txt"))
	assert.Equal(t, "600", mode(t, dir+"/nox"))
	assert.Equal(t, "700", mode(t, dir+"/nox/sub"))
	assert.Equal(t, "640", mode(t, dir+"/a.txt"))
}

func TestExportRefusesPathsOutsideRoot(t *testing.T) {
	t.Parallel()
	addr := server(t)
	c := client(t, addr)
	dir := testDir(t)
	mkdirAll(t, c, dir+"/restore")

	errs := export(t, addr, dir+"/restore", fileRecord("/../outside.txt", "pwned"))

	assert.Error(t, errs["/../outside.txt"], "record escaping the restore root was accepted")
	_, err := c.Stat(dir + "/outside.txt")
	assert.Error(t, err, "outside.txt was written outside the restore root")
}

// Not parallel: it counts the sessions of the whole server.
func TestExporterCloseReleasesConnections(t *testing.T) {
	addr := server(t)
	c := client(t, addr)
	dir := testDir(t)
	mkdirAll(t, c, dir)
	before := sessions(t)

	ctx := t.Context()
	exp, err := exporter.NewExporter(ctx, &connectors.Options{MaxConcurrency: 4}, "ftp", ftpConfig(addr, dir))
	require.NoError(t, err)
	records := make(chan *connectors.Record, 1)
	results := make(chan *connectors.Result, 1)
	records <- fileRecord("/a.txt", "alpha")
	close(records)
	require.NoError(t, exp.Export(ctx, records, results))
	require.NoError(t, (<-results).Err)
	require.Greater(t, sessions(t), before, "export opened no session; the test cannot observe Close")

	require.NoError(t, exp.Close(ctx))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.LessOrEqual(c, sessions(c), before)
	}, 5*time.Second, 50*time.Millisecond, "sessions still open after Close")
	// Otherwise the finalizer on the unreachable socket closes it for us.
	runtime.KeepAlive(exp)
}
