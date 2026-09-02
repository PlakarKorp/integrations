package importer

import (
	"archive/tar"
	"testing"
)

// A hard link names another member of the same archive, so its Linkname is a
// path within the archive.  It used to be passed through untouched and handed
// to the exporter as a symlink target, so "../../etc/shadow" pointed out of
// the restore root.
func TestLinkTargetNormalisesHardLinks(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"other/file", "/other/file"},
		{"../../etc/shadow", "/etc/shadow"},
		{"/etc/shadow", "/etc/shadow"},
		{"a/../../../b", "/b"},
	} {
		got := linkTarget(&tar.Header{Typeflag: tar.TypeLink, Linkname: tc.in})
		if got != tc.want {
			t.Errorf("hard link %q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A symlink target is reproduced verbatim: that is what the archive recorded,
// and the exporter is responsible for not writing through it.
func TestLinkTargetLeavesSymlinksAlone(t *testing.T) {
	for _, in := range []string{"other/file", "../sibling", "/usr/lib/libc.so"} {
		got := linkTarget(&tar.Header{Typeflag: tar.TypeSymlink, Linkname: in})
		if got != in {
			t.Errorf("symlink %q -> %q, want it unchanged", in, got)
		}
	}
}
