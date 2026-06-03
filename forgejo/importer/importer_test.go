package importer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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

func TestImportRunsForgejoDumpAndEmitsRecords(t *testing.T) {
	binary := fakeForgejoBinary(t)
	tempDir := t.TempDir()

	got, err := NewImporter(context.Background(), &connectors.Options{Hostname: "host-a"}, "forgejo", map[string]string{
		"binary":    binary,
		"work_path": "/srv/forgejo",
		"config":    "/etc/forgejo/app.ini",
		"tempdir":   tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	imp := got.(*Importer)
	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	errc := make(chan error, 1)

	go func() {
		errc <- imp.Import(context.Background(), records, results)
	}()

	contents := make(map[string]string)
	for record := range records {
		data, err := io.ReadAll(record.Reader)
		if err != nil {
			t.Fatal(err)
		}
		contents[record.Pathname] = string(data)
		results <- record.Ok()
	}

	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(contents["/manifest.json"], `"forgejo_version": "Forgejo fake 1.0"`) {
		t.Fatalf("manifest missing fake version: %s", contents["/manifest.json"])
	}
	if got := strings.ReplaceAll(contents["/forgejo-dump.zip"], "\r\n", "\n"); got != "fake forgejo dump\n" {
		t.Fatalf("dump content = %q, want fake dump", got)
	}
}

func fakeForgejoBinary(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		binary := filepath.Join(dir, "forgejo.cmd")
		wrapper := `@echo off
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0forgejo.ps1" %*
exit /b %ERRORLEVEL%
`
		script := `$argv = $args
if ($argv.Count -gt 0 -and $argv[0] -eq "--version") {
  Write-Output "Forgejo fake 1.0"
  exit 0
}
$dumpIndex = -1
for ($i = 0; $i -lt $argv.Count; $i++) {
  if ($argv[$i] -eq "dump") {
    $dumpIndex = $i
    break
  }
}
if ($dumpIndex -lt 0) { exit 2 }
$outfile = $null
for ($i = $dumpIndex + 1; $i -lt $argv.Count; $i++) {
  if ($argv[$i] -eq "--file" -and ($i + 1) -lt $argv.Count) {
    $outfile = $argv[$i + 1]
    break
  }
}
if (-not $outfile) { exit 2 }
[System.IO.File]::WriteAllText($outfile, "fake forgejo dump" + [System.Environment]::NewLine)
`
		if err := os.WriteFile(binary, []byte(wrapper), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "forgejo.ps1"), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		return binary
	}

	binary := filepath.Join(dir, "forgejo")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "Forgejo fake 1.0"
  exit 0
fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = "dump" ]; then
    shift
    break
  fi
  shift
done
outfile=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--file" ]; then
    shift
    outfile="$1"
    break
  fi
  shift
done
if [ -z "$outfile" ]; then
  exit 2
fi
printf 'fake forgejo dump\n' > "$outfile"
`
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return binary
}
