package integration

import (
	"context"
	"io"
	"net/url"
	"path"
	"testing"

	"github.com/PlakarKorp/integrations/sftp"
	"github.com/stretchr/testify/require"
)

func TestConnectReadsServerFiles(t *testing.T) {
	s := getServer(t)

	dir := path.Join(home, t.Name())
	_, err := run(context.Background(), s.ctr, "sh", "-c", `mkdir -p "$1" && printf hello > "$1/greeting"`, "sh", dir)
	require.NoError(t, err)

	endpoint := &url.URL{Scheme: "sftp", Host: s.host + ":" + s.port, Path: dir}
	client, err := sftp.Connect(endpoint, s.params())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	entries, err := client.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "greeting", entries[0].Name())

	f, err := client.Open(path.Join(dir, "greeting"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "hello", string(content))
}
