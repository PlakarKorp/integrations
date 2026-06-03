package importer

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
)

func TestNewImporterConfig(t *testing.T) {
	got, err := NewImporter(context.Background(), &connectors.Options{Hostname: "host-a"}, "forgejo", map[string]string{
		"location":    "forgejo:///var/lib/forgejo",
		"work_path":   "/srv/forgejo",
		"config":      "/etc/forgejo/app.ini",
		"custom_path": "/srv/forgejo/custom",
		"tempdir":     "/tmp",
		"type":        "tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}

	imp := got.(*Importer)
	if imp.binary != "forgejo" {
		t.Fatalf("binary = %q, want forgejo", imp.binary)
	}
	if imp.workPath != "/srv/forgejo" {
		t.Fatalf("workPath = %q, want work_path override", imp.workPath)
	}
	if imp.dumpType != "tar.gz" {
		t.Fatalf("dumpType = %q, want tar.gz", imp.dumpType)
	}
	if imp.Origin() != "/srv/forgejo" {
		t.Fatalf("Origin() = %q, want /srv/forgejo", imp.Origin())
	}
}

func TestGlobalArgs(t *testing.T) {
	imp := &Importer{
		workPath:   "/srv/forgejo",
		configPath: "/etc/forgejo/app.ini",
		customPath: "/srv/forgejo/custom",
	}

	want := []string{
		"--work-path", "/srv/forgejo",
		"--config", "/etc/forgejo/app.ini",
		"--custom-path", "/srv/forgejo/custom",
	}
	if got := imp.globalArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("globalArgs() = %#v, want %#v", got, want)
	}
}

func TestArchiveExtension(t *testing.T) {
	tests := map[string]string{
		"":        "zip",
		"zip":     "zip",
		"tar":     "tar",
		"tar.gz":  "tar.gz",
		"tar.xz":  "tar.xz",
		"unknown": "zip",
	}

	for dumpType, want := range tests {
		if got := archiveExtension(dumpType); got != want {
			t.Fatalf("archiveExtension(%q) = %q, want %q", dumpType, got, want)
		}
	}
}

func TestSendMemoryRecordPropagatesAckError(t *testing.T) {
	records := make(chan *connectors.Record, 1)
	results := make(chan *connectors.Result, 1)
	want := errors.New("ack failed")

	go func() {
		record := <-records
		if record.Pathname != "/manifest.json" {
			t.Errorf("Pathname = %q, want /manifest.json", record.Pathname)
		}
		results <- &connectors.Result{Record: *record, Err: want}
	}()

	err := sendMemoryRecord(context.Background(), records, results, "/manifest.json", []byte("{}"))
	if !errors.Is(err, want) {
		t.Fatalf("sendMemoryRecord() error = %v, want %v", err, want)
	}
}
