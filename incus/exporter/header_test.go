package exporter

import (
	"archive/tar"
	"io/fs"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

func rec(path string, fi objects.FileInfo, target string) *connectors.Record {
	return &connectors.Record{Pathname: path, Target: target, FileInfo: fi}
}

func TestTarHeaderRegular(t *testing.T) {
	fi := objects.FileInfo{Lname: "hostname", Lsize: 5, Lmode: fs.FileMode(0644),
		LmodTime: time.Unix(1750000000, 0), Luid: 0, Lgid: 0}
	hdr, err := tarHeader(rec("/backup/container/rootfs/etc/hostname", fi, ""))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Name != "backup/container/rootfs/etc/hostname" || hdr.Typeflag != tar.TypeReg {
		t.Fatalf("bad header: %+v", hdr)
	}
	if hdr.Size != 5 || hdr.Mode != 0644 {
		t.Fatalf("bad size/mode: %+v", hdr)
	}
}

func TestTarHeaderDir(t *testing.T) {
	fi := objects.FileInfo{Lname: "etc", Lmode: fs.ModeDir | 0755}
	hdr, err := tarHeader(rec("/backup/container/rootfs/etc", fi, ""))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Typeflag != tar.TypeDir || hdr.Name != "backup/container/rootfs/etc/" {
		t.Fatalf("bad dir header: %+v", hdr)
	}
	if hdr.Size != 0 {
		t.Fatalf("dir size must be 0: %+v", hdr)
	}
}

func TestTarHeaderSymlink(t *testing.T) {
	fi := objects.FileInfo{Lname: "bin", Lmode: fs.ModeSymlink | 0777}
	hdr, err := tarHeader(rec("/backup/container/rootfs/bin", fi, "usr/bin"))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Typeflag != tar.TypeSymlink || hdr.Linkname != "usr/bin" || hdr.Size != 0 {
		t.Fatalf("bad symlink header: %+v", hdr)
	}
}
