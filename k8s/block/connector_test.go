package block

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/require"
)

func TestImport(t *testing.T) {
	var (
		dest = filepath.Join(t.TempDir(), "import")
		body = "hello world\n"
	)

	require.NoError(t, os.WriteFile(dest, []byte(body), 0600))

	var (
		b = &Block{device: dest}

		records = make(chan *connectors.Record, 1)
		results = make(chan *connectors.Result, 1)
		errch   = make(chan error, 1)
		done    = make(chan struct{})

		rs []connectors.Record
	)

	go func() { errch <- b.Import(t.Context(), records, results) }()

	go func() {
		defer close(done)
		for record := range records {
			rs = append(rs, *record)
			results <- &connectors.Result{Record: *record}
		}
	}()

	<-done
	require.NoError(t, <-errch, "importer failed")
	require.Len(t, rs, 1, "received unexpected number of records")

	require.Equal(t, int64(len(body)), rs[0].FileInfo.Lsize)
}

func TestExporter(t *testing.T) {
	var (
		dest = filepath.Join(t.TempDir(), "import")
		body = "hello world\n"
		rd   = io.NopCloser(strings.NewReader(body))
		b    = &Block{device: dest}

		records = make(chan *connectors.Record, 1)
		results = make(chan *connectors.Result, 1)
	)

	fp, err := os.Create(dest)
	require.NoError(t, err)

	records <- connectors.NewRecord(diskpath, "", objects.FileInfo{
		Lsize: int64(len(body)),
	}, nil, func() (io.ReadCloser, error) { return rd, nil })
	close(records)

	require.NoError(t, b.Export(t.Context(), records, results))

	var result *connectors.Result
	select {
	case result = <-results:
		break
	default:
		t.Error("expected a result")
	}

	require.Nil(t, result.Err)

	sb, err := fp.Stat()
	require.NoError(t, err)

	require.Equal(t, int64(len(body)), sb.Size())
}
