package importer

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
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

// makeTarWithXattr builds a single-file tarball whose entry carries a
// binary-ish PAX xattr record, as GNU/BSD tar would encode
// security.capability.
func makeTarWithXattr(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := "#!/bin/true\n"
	hdr := tar.Header{
		Name:     "backup/container/rootfs/bin/ping",
		Mode:     0755,
		Typeflag: tar.TypeReg,
		Size:     int64(len(body)),
		PAXRecords: map[string]string{
			"SCHILY.xattr.security.capability": "\x01\x00\x00\x02\x00\x20\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00",
		},
	}
	if err := tw.WriteHeader(&hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	return buf.Bytes()
}

// TestImportEmitsXattrRecord covers audit finding #2: a PAX
// "SCHILY.xattr.*" record on a tar entry must be replayed as its own
// kloset xattr Record, immediately after the owning file record and
// sharing its Pathname (see emitXattrs's doc comment for why this
// order is load-bearing).
func TestImportEmitsXattrRecord(t *testing.T) {
	src := &fakeSource{tarball: makeTarWithXattr(t)}
	imp := newImporterWithSource("test", src)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	done := make(chan error, 1)
	go func() { done <- imp.Import(context.Background(), records, results) }()

	var got []*connectors.Record
	for rec := range records {
		got = append(got, rec)
		results <- rec.Ok()
	}
	if err := <-done; err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("want 2 records (file + xattr), got %d", len(got))
	}

	file, xattr := got[0], got[1]
	if file.IsXattr {
		t.Fatalf("first record must be the file record, got IsXattr=true: %+v", file)
	}
	if !xattr.IsXattr {
		t.Fatalf("second record must be the xattr record, got IsXattr=false: %+v", xattr)
	}
	if xattr.Pathname != file.Pathname {
		t.Fatalf("xattr record pathname %q != file record pathname %q", xattr.Pathname, file.Pathname)
	}
	if want := "/backup/container/rootfs/bin/ping"; file.Pathname != want {
		t.Fatalf("file pathname: got %q, want %q", file.Pathname, want)
	}
	if xattr.XattrName != "security.capability" {
		t.Fatalf("xattr name: got %q, want %q", xattr.XattrName, "security.capability")
	}
	if xattr.XattrType != objects.AttributeExtended {
		t.Fatalf("xattr type: got %v, want AttributeExtended", xattr.XattrType)
	}
	value, err := io.ReadAll(xattr.Reader)
	if err != nil {
		t.Fatal(err)
	}
	want := "\x01\x00\x00\x02\x00\x20\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"
	if string(value) != want {
		t.Fatalf("xattr value: got %q, want %q", value, want)
	}
}
