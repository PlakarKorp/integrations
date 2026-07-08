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

func TestFinfoDir(t *testing.T) {
	hdr := &tar.Header{Name: "backup/container/rootfs/etc/", Mode: 0755, Typeflag: tar.TypeDir}
	if fi := finfo(hdr); fi.Lmode&fs.ModeDir == 0 {
		t.Fatalf("dir flag missing: %v", fi.Lmode)
	}
}

func TestFinfoSymlink(t *testing.T) {
	hdr := &tar.Header{Name: "backup/container/rootfs/bin", Linkname: "usr/bin", Typeflag: tar.TypeSymlink}
	if fi := finfo(hdr); fi.Lmode&fs.ModeSymlink == 0 {
		t.Fatalf("symlink flag missing: %v", fi.Lmode)
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
