package exporter

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

type fakeSink struct {
	instance string
	tarball  bytes.Buffer
}

func (f *fakeSink) Ping(ctx context.Context) error { return nil }

func (f *fakeSink) Restore(ctx context.Context, instance string, tarStream io.Reader) error {
	f.instance = instance
	_, err := io.Copy(&f.tarball, tarStream)
	return err
}

func TestExportRebuildsTar(t *testing.T) {
	sink := &fakeSink{}
	exp := newExporterWithSink("restored-1", sink)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result, 8)
	done := make(chan error, 1)
	go func() { done <- exp.Export(context.Background(), records, results) }()

	feed := []*connectors.Record{
		{Pathname: "/backup/index.yaml",
			FileInfo: objects.FileInfo{Lname: "index.yaml", Lsize: 11, Lmode: 0644, LmodTime: time.Unix(1750000000, 0)},
			Reader:   io.NopCloser(strings.NewReader("name: test\n"))},
		{Pathname: "/backup/container/rootfs/etc",
			FileInfo: objects.FileInfo{Lname: "etc", Lmode: fs.ModeDir | 0755}},
		{Pathname: "/backup/container/rootfs/etc/hostname",
			FileInfo: objects.FileInfo{Lname: "hostname", Lsize: 5, Lmode: 0644},
			Reader:   io.NopCloser(strings.NewReader("test\n"))},
		{Pathname: "/backup/container/rootfs/bin", Target: "usr/bin",
			FileInfo: objects.FileInfo{Lname: "bin", Lmode: fs.ModeSymlink | 0777}},
	}
	for _, r := range feed {
		records <- r
	}
	close(records)
	if err := <-done; err != nil {
		t.Fatalf("Export: %v", err)
	}

	if sink.instance != "restored-1" {
		t.Fatalf("instance: %q", sink.instance)
	}

	// results: un ack par record (le contenu de Result est opaque au SDK ;
	// l'erreur globale d'Export est le vrai signal)
	for range len(feed) {
		<-results
	}

	// re-lire le tar produit
	tr := tar.NewReader(bytes.NewReader(sink.tarball.Bytes()))
	var names []string
	var hostname string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		if hdr.Name == "backup/container/rootfs/etc/hostname" {
			b, _ := io.ReadAll(tr)
			hostname = string(b)
		}
		if hdr.Name == "backup/container/rootfs/bin" && hdr.Linkname != "usr/bin" {
			t.Fatalf("symlink lost: %+v", hdr)
		}
	}
	want := []string{"backup/index.yaml", "backup/container/rootfs/etc/", "backup/container/rootfs/etc/hostname", "backup/container/rootfs/bin"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("entries: %v", names)
	}
	if hostname != "test\n" {
		t.Fatalf("hostname content: %q", hostname)
	}
}
