package exporter

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

func TestExporterWritesDumpRDB(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "dump.rdb")
	exp, err := New("redis-file", map[string]string{"output": out})
	if err != nil {
		t.Fatal(err)
	}
	records := make(chan *connectors.Record, 1)
	results := make(chan *connectors.Result, 1)
	records <- connectors.NewRecord("/dump.rdb", "", objects.FileInfo{Lname: "dump.rdb", Lmode: 0444, LmodTime: time.Now()}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(&readString{s: "REDIS0009"}), nil
	})
	close(records)
	if err := exp.Export(context.Background(), records, results); err != nil {
		t.Fatal(err)
	}
	res := <-results
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "REDIS0009" {
		t.Fatalf("unexpected restored content %q", string(got))
	}
}

func TestExporterRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "dump.rdb")
	if err := os.WriteFile(out, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	exp, err := New("redis-file", map[string]string{"output": out})
	if err != nil {
		t.Fatal(err)
	}
	records := make(chan *connectors.Record, 1)
	results := make(chan *connectors.Result, 1)
	records <- connectors.NewRecord("/dump.rdb", "", objects.FileInfo{Lname: "dump.rdb", Lmode: 0444, LmodTime: time.Now()}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(&readString{s: "new"}), nil
	})
	close(records)
	if err := exp.Export(context.Background(), records, results); err != nil {
		t.Fatal(err)
	}
	res := <-results
	if res.Err == nil {
		t.Fatal("expected overwrite refusal")
	}
}

type readString struct{ s string }

func (r *readString) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}
