package importer

import (
	"archive/tar"
	"io/fs"
	"testing"
	"time"
)

func TestFinfoRegular(t *testing.T) {
	hdr := &tar.Header{
		Name: "backup/container/rootfs/etc/hostname",
		Size: 7, Mode: 0644, Uid: 0, Gid: 0,
		ModTime: time.Unix(1750000000, 0), Typeflag: tar.TypeReg,
	}
	fi := finfo(hdr)
	if fi.Lname != "hostname" || fi.Lsize != 7 || fi.Lmode != fs.FileMode(0644) {
		t.Fatalf("bad fileinfo: %+v", fi)
	}
	if fi.Luid != 0 || fi.Lgid != 0 || !fi.LmodTime.Equal(time.Unix(1750000000, 0)) {
		t.Fatalf("bad owner/time: %+v", fi)
	}
}

func TestFinfoSpecialBits(t *testing.T) {
	hdr := &tar.Header{
		Name: "backup/container/rootfs/usr/bin/sudo",
		Size: 3, Mode: 0o4755, Typeflag: tar.TypeReg,
	}
	fi := finfo(hdr)
	want := fs.ModeSetuid | fs.FileMode(0o755)
	if fi.Lmode != want {
		t.Fatalf("setuid bit not converted: got mode %o (%v), want %o (%v)", fi.Lmode, fi.Lmode, want, want)
	}
}

func TestFinfoDir(t *testing.T) {
	hdr := &tar.Header{Name: "backup/container/rootfs/etc/", Mode: 0755, Typeflag: tar.TypeDir}
	if fi := finfo(hdr); fi.Lmode&fs.ModeDir == 0 {
		t.Fatalf("dir flag missing: %v", fi.Lmode)
	}
}

func TestFinfoSymlink(t *testing.T) {
	hdr := &tar.Header{Name: "backup/container/rootfs/bin", Linkname: "usr/bin", Typeflag: tar.TypeSymlink}
	fi := finfo(hdr)
	if fi.Lmode&fs.ModeSymlink == 0 {
		t.Fatalf("symlink flag missing: %v", fi.Lmode)
	}
	if fi.Lnlink > 1 {
		t.Fatalf("plain symlink must not be flagged as hardlink: Lnlink=%d", fi.Lnlink)
	}
	if got := linkTarget(hdr); got != "usr/bin" {
		t.Fatalf("plain symlink target must stay untouched: got %q, want %q", got, "usr/bin")
	}
}

func TestFinfoHardlink(t *testing.T) {
	hdr := &tar.Header{
		Typeflag: tar.TypeLink,
		Name:     "backup/container/rootfs/bin/ls",
		Linkname: "backup/container/rootfs/bin/busybox",
	}
	fi := finfo(hdr)
	if fi.Lmode&fs.ModeSymlink == 0 {
		t.Fatalf("hardlink must still map to fs.ModeSymlink: %v", fi.Lmode)
	}
	if fi.Lnlink != 2 {
		t.Fatalf("hardlink must be flagged via Lnlink=2, got %d", fi.Lnlink)
	}
	// Target must be relative to the link's own directory so that any
	// generic exporter materializes a symlink resolving inside the
	// restored tree.
	if got := linkTarget(hdr); got != "busybox" {
		t.Fatalf("hardlink target: got %q, want %q (relative to link dir)", got, "busybox")
	}
}

func TestFinfoHardlinkCrossDir(t *testing.T) {
	hdr := &tar.Header{
		Typeflag: tar.TypeLink,
		Name:     "backup/container/rootfs/bin/ls",
		Linkname: "backup/container/rootfs/sbin/tool",
	}
	want := "../sbin/tool"
	if got := linkTarget(hdr); got != want {
		t.Fatalf("cross-dir hardlink target: got %q, want %q", got, want)
	}
}

func TestRelPath(t *testing.T) {
	cases := []struct {
		fromDir, to, want string
	}{
		{"/backup/container/rootfs/bin", "/backup/container/rootfs/bin/busybox", "busybox"},
		{"/backup/container/rootfs/bin", "/backup/container/rootfs/sbin/tool", "../sbin/tool"},
		{"/backup/container/rootfs/usr/bin", "/backup/container/rootfs/bin/sh", "../../bin/sh"},
		{"/", "/backup/index.yaml", "backup/index.yaml"},
	}
	for _, c := range cases {
		if got := relPath(c.fromDir, c.to); got != c.want {
			t.Fatalf("relPath(%q, %q) = %q, want %q", c.fromDir, c.to, got, c.want)
		}
	}
}

func TestFinfoDevice(t *testing.T) {
	hdr := &tar.Header{
		Name: "backup/container/rootfs/dev/null", Typeflag: tar.TypeChar,
		Mode: 0666, Devmajor: 1, Devminor: 3,
	}
	fi := finfo(hdr)
	if fi.Lmode&fs.ModeCharDevice == 0 {
		t.Fatalf("char device flag missing: %v", fi.Lmode)
	}
	wantDev := uint64(1)<<32 | 3
	if fi.Ldev != wantDev {
		t.Fatalf("device major/minor: got Ldev=%#x, want %#x", fi.Ldev, wantDev)
	}
}

func TestRecordPath(t *testing.T) {
	hdr := &tar.Header{Name: "backup/index.yaml"}
	if p := recordPath(hdr); p != "/backup/index.yaml" {
		t.Fatalf("got %q", p)
	}
	hdr = &tar.Header{Name: "./backup/container/rootfs/etc/"}
	if p := recordPath(hdr); p != "/backup/container/rootfs/etc" {
		t.Fatalf("got %q", p)
	}
}
