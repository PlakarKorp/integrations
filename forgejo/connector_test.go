package forgejo

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

func TestNewConnectorDefaults(t *testing.T) {
	conn, err := newConnector(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if conn.forgejoBin != "forgejo" {
		t.Fatalf("forgejoBin = %q, want forgejo", conn.forgejoBin)
	}
	if conn.timeout != 30*time.Minute {
		t.Fatalf("timeout = %s, want 30m", conn.timeout)
	}
}

func TestNewConnectorCustomTimeout(t *testing.T) {
	conn, err := newConnector(map[string]string{
		"forgejo_bin": "/usr/local/bin/forgejo",
		"timeout":     "2m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn.forgejoBin != "/usr/local/bin/forgejo" {
		t.Fatalf("forgejoBin = %q", conn.forgejoBin)
	}
	if conn.timeout != 2*time.Minute {
		t.Fatalf("timeout = %s, want 2m", conn.timeout)
	}
}

func TestNewConnectorInvalidTimeout(t *testing.T) {
	if _, err := newConnector(map[string]string{"timeout": "soon"}); err == nil {
		t.Fatal("expected invalid timeout error")
	}
}

func TestExporterWritesForgejoDump(t *testing.T) {
	outDir := t.TempDir()
	exp, err := NewExporter(context.Background(), &connectors.Options{}, "forgejo", map[string]string{
		"output_dir": outDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	records := make(chan *connectors.Record, 1)
	results := make(chan *connectors.Result, 1)
	records <- connectors.NewRecord(dumpPath, "", objects.FileInfo{Lmode: 0o444}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("forgejo dump bytes")), nil
	})
	close(records)

	if err := exp.Export(context.Background(), records, results); err != nil {
		t.Fatal(err)
	}
	result := <-results
	if result.Err != nil {
		t.Fatal(result.Err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, filepath.Base(dumpPath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "forgejo dump bytes" {
		t.Fatalf("dump contents = %q", got)
	}
}
