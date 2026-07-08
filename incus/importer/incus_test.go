package importer

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
)

type fakeSource struct {
	tarball []byte
	cleaned bool
	pinged  bool
}

func (f *fakeSource) Ping(ctx context.Context) error { f.pinged = true; return nil }

func (f *fakeSource) Open(ctx context.Context, instance string) (io.ReadCloser, func() error, error) {
	return io.NopCloser(bytes.NewReader(f.tarball)), func() error { f.cleaned = true; return nil }, nil
}

// makeTar builds an incus-export-shaped tarball in memory.
func makeTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	entries := []struct {
		hdr  tar.Header
		body string
	}{
		{tar.Header{Name: "backup/index.yaml", Mode: 0644, Typeflag: tar.TypeReg, Size: 12}, "name: test\n_"},
		{tar.Header{Name: "backup/container/rootfs/etc/", Mode: 0755, Typeflag: tar.TypeDir}, ""},
		{tar.Header{Name: "backup/container/rootfs/etc/hostname", Mode: 0644, Typeflag: tar.TypeReg, Size: 5}, "test\n"},
		{tar.Header{Name: "backup/container/rootfs/bin", Linkname: "usr/bin", Typeflag: tar.TypeSymlink}, ""},
	}
	for _, e := range entries {
		if err := tw.WriteHeader(&e.hdr); err != nil {
			t.Fatal(err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	return buf.Bytes()
}

func TestImportEmitsRecords(t *testing.T) {
	src := &fakeSource{tarball: makeTar(t)}
	imp := newImporterWithSource("test", src)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	done := make(chan error, 1)
	go func() { done <- imp.Import(context.Background(), records, results) }()

	var got []*connectors.Record
	var contents []string
	for rec := range records {
		got = append(got, rec)
		if rec.Reader != nil && rec.FileInfo.Lmode.IsRegular() {
			b, err := io.ReadAll(rec.Reader)
			if err != nil {
				t.Fatal(err)
			}
			contents = append(contents, string(b))
		}
		results <- rec.Ok() // ack, comme le fait kloset
	}
	if err := <-done; err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("want 4 records, got %d", len(got))
	}
	if got[0].Pathname != "/backup/index.yaml" {
		t.Fatalf("first record: %q", got[0].Pathname)
	}
	if got[3].Target != "usr/bin" {
		t.Fatalf("symlink target: %q", got[3].Target)
	}
	if len(contents) != 2 || contents[1] != "test\n" {
		t.Fatalf("contents: %q", contents)
	}
	if !src.cleaned {
		t.Fatal("incus backup not cleaned up")
	}
}
