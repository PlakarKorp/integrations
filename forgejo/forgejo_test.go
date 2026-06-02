package forgejo

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
)

func TestImporterDumpArgs(t *testing.T) {
	cfg, err := parseImporterConfig(map[string]string{
		"location":             "forgejo://local",
		"forgejo_bin":          "/usr/bin/forgejo",
		"work_path":            "/var/lib/forgejo",
		"custom_path":          "/var/lib/forgejo/custom",
		"config_path":          "/etc/forgejo/app.ini",
		"temp_dir":             "/tmp",
		"database":             "postgres",
		"dump_type":            "tar.gz",
		"skip_repository":      "true",
		"skip_log":             "true",
		"skip_custom_dir":      "true",
		"skip_lfs_data":        "true",
		"skip_attachment_data": "true",
		"skip_package_data":    "true",
		"skip_index":           "true",
		"skip_repo_archives":   "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	importer := &Importer{cfg: cfg}
	got := importer.dumpArgs()
	want := []string{
		"dump",
		"--file", "-",
		"--type", "tar.gz",
		"--quiet",
		"--work-path", "/var/lib/forgejo",
		"--custom-path", "/var/lib/forgejo/custom",
		"--config", "/etc/forgejo/app.ini",
		"--tempdir", "/tmp",
		"--database", "postgres",
		"--skip-repository",
		"--skip-log",
		"--skip-custom-dir",
		"--skip-lfs-data",
		"--skip-attachment-data",
		"--skip-package-data",
		"--skip-index",
		"--skip-repo-archives",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dump args mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestImporterNormalizesDumpType(t *testing.T) {
	cfg, err := parseImporterConfig(map[string]string{
		"dump_type": "TGZ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dumpType != "tgz" {
		t.Fatalf("unexpected dump type: %q", cfg.dumpType)
	}
}

func TestImporterImportReportsClosedResults(t *testing.T) {
	importer := &Importer{cfg: config{dumpType: defaultDumpType}}
	records := make(chan *connectors.Record, 1)
	results := make(chan *connectors.Result)
	close(results)

	err := importer.Import(context.Background(), records, results)
	if err == nil {
		t.Fatal("expected an error when results closes without acknowledgement")
	}
}

func TestExporterTargetDirFromLocation(t *testing.T) {
	cfg, err := parseExporterConfig(map[string]string{
		"location": "forgejo:///tmp/forgejo-restore",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.targetDir != "/tmp/forgejo-restore" {
		t.Fatalf("unexpected target dir: %q", cfg.targetDir)
	}
}

func TestExtractTarGz(t *testing.T) {
	target := t.TempDir()
	var archive bytes.Buffer

	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "custom/conf/app.ini", Mode: 0644, Size: int64(len("APP_NAME = Forgejo\n"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("APP_NAME = Forgejo\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	if err := extractTarGz(bytes.NewReader(archive.Bytes()), target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "custom", "conf", "app.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "APP_NAME = Forgejo\n" {
		t.Fatalf("unexpected file contents: %q", got)
	}
}

func TestExtractTarRejectsTraversal(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := extractTar(bytes.NewReader(archive.Bytes()), t.TempDir()); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestExtractZip(t *testing.T) {
	target := t.TempDir()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	writer, err := zw.Create("repositories/example.git/HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("ref: refs/heads/main\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := extractZip(bytes.NewReader(archive.Bytes()), target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "repositories", "example.git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ref: refs/heads/main\n" {
		t.Fatalf("unexpected file contents: %q", got)
	}
}
