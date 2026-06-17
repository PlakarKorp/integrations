package exporter

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

func TestNewExporterConfig(t *testing.T) {
	got, err := NewExporter(context.Background(), nil, "forgejo", map[string]string{
		"location": "forgejo:///tmp/forgejo-restore",
	})
	if err != nil {
		t.Fatal(err)
	}

	exp := got.(*Exporter)
	if !strings.HasSuffix(filepath.ToSlash(exp.outputDir), "/tmp/forgejo-restore") {
		t.Fatalf("outputDir = %q, want path from location", exp.outputDir)
	}

	if _, err := NewExporter(context.Background(), nil, "forgejo", map[string]string{}); err == nil {
		t.Fatal("NewExporter() succeeded without output dir")
	}
}

func TestWriteRecordWritesFile(t *testing.T) {
	dir := t.TempDir()
	exp := &Exporter{outputDir: dir}
	content := "forgejo dump contents"

	record := connectors.NewRecord("/nested/forgejo-dump.zip", "", objects.FileInfo{
		Lname:    "forgejo-dump.zip",
		Lsize:    int64(len(content)),
		Lmode:    0640,
		LmodTime: time.Now(),
		Lnlink:   1,
	}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	})

	if err := exp.writeRecord(record); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "nested", "forgejo-dump.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("restored content = %q, want %q", string(got), content)
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	for _, pathname := range []string{"../escape", "/../escape", "nested/../../escape", `nested\..\escape`} {
		if _, err := safeJoin(dir, pathname); err == nil {
			t.Fatalf("safeJoin(%q) succeeded, want traversal rejection", pathname)
		}
	}
}

func TestSafeJoinAllowsNormalPath(t *testing.T) {
	dir := t.TempDir()
	got, err := safeJoin(dir, "/nested/forgejo-dump.zip")
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dir, "nested", "forgejo-dump.zip")
	if got != want {
		t.Fatalf("safeJoin() = %q, want %q", got, want)
	}
}
