package exporter

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

func hardlink() *connectors.Record {
	return &connectors.Record{
		Reader: io.NopCloser(strings.NewReader("hello")),
		FileInfo: objects.FileInfo{
			Lmode:    0644,
			LmodTime: time.Now(),
			Ldev:     1,
			Lino:     42,
			Luid:     1000,
			Lgid:     1000,
			Lnlink:   2,
		},
	}
}

// TestHardlinks exercises restoring multiple hardlinks.
func TestHardlinks(t *testing.T) {
	dir := t.TempDir()
	p := &FSExporter{rootDir: dir}

	canonPath := filepath.Join(dir, "canon")
	if err := p.file(hardlink(), canonPath); err != nil {
		t.Fatalf("writing the canonical copy: %v", err)
	}

	// The singleflight group for this dev:ino has already returned, so this
	// is a brand new Do() call that has to consult hlCanon.
	linkPath := filepath.Join(dir, "link")
	if err := p.file(hardlink(), linkPath); err != nil {
		t.Fatalf("restoring the second copy of a hardlinked file failed: %v", err)
	}

	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("hardlink target was never created: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s is a symlink, want a hardlink", linkPath)
	}

	canonSt, err := os.Stat(canonPath)
	if err != nil {
		t.Fatal(err)
	}
	linkSt, err := os.Stat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(canonSt, linkSt) {
		t.Errorf("%s and %s are not the same inode", canonPath, linkPath)
	}
}
