package integration

import (
	"testing"

	"github.com/PlakarKorp/integrations/ftp/importer"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportReadsTree(t *testing.T) {
	t.Parallel()
	addr := server(t)
	c := client(t, addr)
	dir := testDir(t)

	want := map[string]string{
		dir + "/a.txt":            "alpha",
		dir + "/sub/b.txt":        "bravo",
		dir + "/sub/deeper/c.txt": "charlie",
		dir + "/sub/deeper/empty": "",
	}
	for name, content := range want {
		put(t, c, name, content)
	}

	ctx := t.Context()
	imp, err := importer.NewImporter(ctx, &connectors.Options{}, "ftp", ftpConfig(addr, dir))
	require.NoError(t, err)
	defer func() { _ = imp.Close(ctx) }()

	records := make(chan *connectors.Record)
	errc := make(chan error, 1)
	go func() { errc <- imp.Import(ctx, records, nil) }()

	got := map[string]string{}
	for rec := range records {
		if !assert.NoError(t, rec.Err, rec.Pathname) {
			continue
		}
		assert.True(t, rec.FileInfo.Lmode.IsRegular(), "%s: mode %v, want a regular file", rec.Pathname, rec.FileInfo.Lmode)
		got[rec.Pathname] = readAll(t, rec.Reader)
	}
	require.NoError(t, <-errc)

	assert.Equal(t, want, got)
}

// A backup that cannot list its root must fail, not succeed empty.
func TestImportFailsWhenRootUnreadable(t *testing.T) {
	t.Parallel()
	addr := server(t)

	cfg := ftpConfig(addr, "/")
	delete(cfg, "tls_insecure_no_verify") // the self-signed certificate is refused

	ctx := t.Context()
	imp, err := importer.NewImporter(ctx, &connectors.Options{}, "ftp", cfg)
	require.NoError(t, err)
	defer func() { _ = imp.Close(ctx) }()

	records := make(chan *connectors.Record)
	errc := make(chan error, 1)
	go func() { errc <- imp.Import(ctx, records, nil) }()
	for rec := range records {
		assert.Failf(t, "unexpected record", "%s", rec.Pathname)
	}
	require.Error(t, <-errc, "Import succeeded without being able to read the root")
}

func TestImportReportsUnreadableDirectory(t *testing.T) {
	t.Parallel()
	addr := server(t)
	c := client(t, addr)

	dir := testDir(t)

	put(t, c, dir+"/ok.txt", "fine")
	put(t, c, dir+"/locked/secret.txt", "hidden")
	// pure-ftpd refuses to let a user lock themselves out, so do it as root.
	chmod(t, dir+"/locked", "000")
	t.Cleanup(func() { chmod(t, dir+"/locked", "755") })

	ctx := t.Context()
	imp, err := importer.NewImporter(ctx, &connectors.Options{}, "ftp", ftpConfig(addr, dir))
	require.NoError(t, err)
	defer func() { _ = imp.Close(ctx) }()

	records := make(chan *connectors.Record)
	errc := make(chan error, 1)
	go func() { errc <- imp.Import(ctx, records, nil) }()

	got := map[string]error{}
	for rec := range records {
		got[rec.Pathname] = rec.Err
		if rec.Reader != nil {
			_ = rec.Reader.Close()
		}
	}
	require.NoError(t, <-errc)

	if assert.Contains(t, got, dir+"/ok.txt") {
		assert.NoError(t, got[dir+"/ok.txt"])
	}
	assert.Error(t, got[dir+"/locked"], "no error record for the unreadable directory")
}
