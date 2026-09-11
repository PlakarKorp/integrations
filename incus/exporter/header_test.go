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

// TestTarHeaderSpecialBitsLegacy covers pre-fix snapshots: Lmode carries the
// raw tar special bits directly (no fs.ModeSetuid/Setgid/Sticky flag set).
// The legacy compat OR in tarHeader must still reproduce the original mode.
func TestTarHeaderSpecialBitsLegacy(t *testing.T) {
	fi := objects.FileInfo{Lname: "sudo", Lsize: 3, Lmode: fs.FileMode(0o4755),
		LmodTime: time.Unix(1750000000, 0), Luid: 0, Lgid: 0}
	hdr, err := tarHeader(rec("/backup/container/rootfs/usr/bin/sudo", fi, ""))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Mode != 0o4755 {
		t.Fatalf("setuid bit dropped (legacy raw form): got mode %o, want %o", hdr.Mode, 0o4755)
	}
}

// TestTarHeaderSpecialBitsNewForm covers snapshots taken after the fix:
// Lmode carries the portable fs.ModeSetuid flag plus permission bits.
func TestTarHeaderSpecialBitsNewForm(t *testing.T) {
	fi := objects.FileInfo{Lname: "sudo", Lsize: 3, Lmode: fs.ModeSetuid | fs.FileMode(0o755),
		LmodTime: time.Unix(1750000000, 0), Luid: 0, Lgid: 0}
	hdr, err := tarHeader(rec("/backup/container/rootfs/usr/bin/sudo", fi, ""))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Mode != 0o4755 {
		t.Fatalf("setuid bit not converted (new form): got mode %o, want %o", hdr.Mode, 0o4755)
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

// TestTarHeaderHardlink mirrors what importer.finfo/linkTarget produce for a
// tar.TypeLink entry: Lmode has ModeSymlink set, Lnlink==2, and Target is
// RELATIVE to the record's own directory. The exporter must re-emit
// tar.TypeLink with a tar-root-relative Linkname, not a broken TypeSymlink.
func TestTarHeaderHardlink(t *testing.T) {
	fi := objects.FileInfo{Lname: "ls", Lmode: fs.ModeSymlink | 0777, Lnlink: 2}
	hdr, err := tarHeader(rec("/backup/container/rootfs/bin/ls", fi, "busybox"))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Typeflag != tar.TypeLink {
		t.Fatalf("hardlink must round-trip as tar.TypeLink, got %v", hdr.Typeflag)
	}
	want := "backup/container/rootfs/bin/busybox"
	if hdr.Linkname != want {
		t.Fatalf("hardlink Linkname: got %q, want %q", hdr.Linkname, want)
	}
}

// TestTarHeaderHardlinkCrossDir checks that a "../"-style relative Target is
// resolved against the record's directory back to the exact tar-root path.
func TestTarHeaderHardlinkCrossDir(t *testing.T) {
	fi := objects.FileInfo{Lname: "ls", Lmode: fs.ModeSymlink | 0777, Lnlink: 2}
	hdr, err := tarHeader(rec("/backup/container/rootfs/bin/ls", fi, "../sbin/tool"))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Typeflag != tar.TypeLink {
		t.Fatalf("hardlink must round-trip as tar.TypeLink, got %v", hdr.Typeflag)
	}
	want := "backup/container/rootfs/sbin/tool"
	if hdr.Linkname != want {
		t.Fatalf("cross-dir hardlink Linkname: got %q, want %q", hdr.Linkname, want)
	}
}

func TestTarHeaderDevice(t *testing.T) {
	fi := objects.FileInfo{Lname: "null", Lmode: fs.ModeCharDevice | 0666, Ldev: uint64(1)<<32 | 3}
	hdr, err := tarHeader(rec("/backup/container/rootfs/dev/null", fi, ""))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Typeflag != tar.TypeChar {
		t.Fatalf("device typeflag: got %v, want TypeChar", hdr.Typeflag)
	}
	if hdr.Devmajor != 1 || hdr.Devminor != 3 {
		t.Fatalf("device major/minor: got %d:%d, want 1:3", hdr.Devmajor, hdr.Devminor)
	}
}
