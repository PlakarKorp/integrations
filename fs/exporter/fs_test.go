package exporter

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

func newExporter(t *testing.T, root string) *FSExporter {
	t.Helper()

	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}

	exp, err := NewFSExporter(t.Context(), &connectors.Options{MaxConcurrency: 1},
		"fs", map[string]string{"location": "fs://" + root})
	if err != nil {
		t.Fatalf("NewFSExporter: %v", err)
	}
	t.Cleanup(func() { exp.Close(context.Background()) })

	return exp.(*FSExporter)
}

func fileRecord(pathname, content string) *connectors.Record {
	return &connectors.Record{
		Pathname: pathname,
		Reader:   io.NopCloser(strings.NewReader(content)),
		FileInfo: objects.FileInfo{Lname: filepath.Base(pathname), Lmode: 0644, Lsize: int64(len(content))},
	}
}

func symlinkRecord(pathname, target string) *connectors.Record {
	return &connectors.Record{
		Pathname: pathname,
		Target:   target,
		FileInfo: objects.FileInfo{Lname: filepath.Base(pathname), Lmode: os.ModeSymlink | 0777},
	}
}

func hardlinkRecord(pathname, content string, nlink uint16) *connectors.Record {
	return &connectors.Record{
		Reader:   io.NopCloser(strings.NewReader(content)),
		Pathname: pathname,
		FileInfo: objects.FileInfo{
			Lname:  filepath.Base(pathname),
			Lmode:  0644,
			Ldev:   1,
			Lino:   42,
			Lnlink: nlink,
			Lsize:  int64(len(content)),
		},
	}
}

// run feeds records through Export and returns the per-record errors.
func run(t *testing.T, exp *FSExporter, recs ...*connectors.Record) []error {
	t.Helper()

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result, len(recs))

	go func() {
		defer close(records)
		for _, r := range recs {
			records <- r
		}
	}()

	if err := exp.Export(t.Context(), records, results); err != nil {
		t.Fatalf("Export: %v", err)
	}

	var errs []error
	for res := range results {
		errs = append(errs, res.Err)
	}
	return errs
}

// TestHardlinks exercises restoring multiple hardlinks.
func TestHardlinks(t *testing.T) {
	dir := t.TempDir()
	p := newExporter(t, dir)

	// We reuse dev:ino so that on the second hardlink process the SF group has
	// already returned so this is a brand new Do() call that has to consult
	// hlCanon
	errs := run(t, p, hardlinkRecord("/canon", "hello", 2), hardlinkRecord("/link", "hello", 2))

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("expected no errors got: %+v\n", errs)
	}

	canonPath := filepath.Join(dir, "canon")
	linkPath := filepath.Join(dir, "link")

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

// A record whose pathname climbs out of the restore root must not write
// outside of it.
func TestExportRejectsDotDotPath(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "restore")

	exp := newExporter(t, root)
	run(t, exp, fileRecord("/../escaped", "owned"))

	if _, err := os.Lstat(filepath.Join(base, "escaped")); !os.IsNotExist(err) {
		t.Fatalf("record escaped the restore root: %v", err)
	}
}

// The core regression: a symlink from the archive pointing outside the root,
// followed by a record that writes *through* it.  The lexical containment
// check that used to guard this could not see the second path leaving.
func TestExportDoesNotWriteThroughSymlink(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "restore")

	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}

	exp := newExporter(t, root)
	errs := run(t, exp,
		symlinkRecord("/link", outside),
		fileRecord("/link/pwned", "owned"),
	)

	// The symlink itself is restored faithfully -- that is the snapshot's
	// content -- but writing through it has to fail.
	if errs[0] != nil {
		t.Fatalf("symlink should still be restored verbatim: %v", errs[0])
	}
	if errs[1] == nil {
		t.Fatal("writing through an escaping symlink was allowed")
	}

	if _, err := os.Lstat(filepath.Join(outside, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("wrote through the symlink into %s: %v", outside, err)
	}
}

// Same shape, but the symlink is absolute and the write reaches it through a
// deeper path.
func TestExportDoesNotWriteThroughNestedSymlink(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "restore")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "etc"), 0700); err != nil {
		t.Fatal(err)
	}

	exp := newExporter(t, root)
	dir := &connectors.Record{
		Pathname: "/sub",
		FileInfo: objects.FileInfo{Lname: "sub", Lmode: os.ModeDir | 0755},
	}
	errs := run(t, exp,
		dir,
		symlinkRecord("/sub/link", outside),
		fileRecord("/sub/link/etc/pwned", "owned"),
	)

	if errs[2] == nil {
		t.Fatal("writing through a nested escaping symlink was allowed")
	}
	if _, err := os.Lstat(filepath.Join(outside, "etc", "pwned")); !os.IsNotExist(err) {
		t.Fatalf("wrote through the nested symlink: %v", err)
	}
}

// Ordinary restores keep working: nested dirs, file contents, and a symlink
// that stays inside the root.
func TestExportRestoresNormalTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "restore")
	exp := newExporter(t, root)

	errs := run(t, exp,
		&connectors.Record{
			Pathname: "/dir",
			FileInfo: objects.FileInfo{Lname: "dir", Lmode: os.ModeDir | 0755},
		},
		fileRecord("/dir/hello", "world"),
		symlinkRecord("/dir/rel", "hello"),
	)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("record %d failed: %v", i, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(root, "dir", "hello"))
	if err != nil || string(got) != "world" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}

	target, err := os.Readlink(filepath.Join(root, "dir", "rel"))
	if err != nil || target != "hello" {
		t.Fatalf("Readlink = %q, %v", target, err)
	}
}

func TestRelativeRejectsEscapes(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{in: "/a/b", want: filepath.Join("a", "b"), ok: true},
		{in: "/", want: ".", ok: true},
		{in: "", ok: false},
		{in: "/../../etc/passwd", ok: false},
		{in: "/a/../../b", ok: false},
	} {
		got, err := relative(tc.in)
		if tc.ok {
			if err != nil {
				t.Fatalf("relative(%q): %v", tc.in, err)
			}

			if got != tc.want {
				t.Errorf("relative(%q) = %q, want %q", tc.in, got, tc.want)
			}
		} else if err == nil {
			t.Fatalf("relative(%q): expected failure", tc.in)
		}
	}
}

// setuidRecord returns a file record whose recorded mode carries the
// setuid, setgid and sticky bits, so restoreMode/permissions can be
// exercised against it.
func setuidRecord(pathname, content string) *connectors.Record {
	return &connectors.Record{
		Pathname: pathname,
		Reader:   io.NopCloser(strings.NewReader(content)),
		FileInfo: objects.FileInfo{Lname: filepath.Base(pathname), Lmode: os.ModeSetuid | os.ModeSetgid | 0755, Lsize: int64(len(content))},
	}
}

// TestExport_FileStripsSetuidByDefault verifies that, by default
// (allowPrivilegeEscalation unset), restored files never keep the
// setuid/setgid bits recorded in the snapshot, even if the snapshot
// carried them - preventing restoring an attacker-controlled privileged
// executable.
func TestExport_FileStripsSetuidByDefault(t *testing.T) {
	root := filepath.Join(t.TempDir(), "restore")
	exp := newExporter(t, root)

	errs := run(t, exp, setuidRecord("/file", "content"))
	if errs[0] != nil {
		t.Fatalf("unexpected error: %v", errs[0])
	}

	fi, err := os.Stat(filepath.Join(root, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		t.Fatalf("setuid/setgid bits must be stripped by default, got mode %v", fi.Mode())
	}
	if fi.Mode().Perm() != 0755 {
		t.Fatalf("unexpected permission bits: got %v, want 0755", fi.Mode().Perm())
	}
}

// TestExport_FilePreservesSetuidWhenAllowed verifies the opt-in path: when
// allowPrivilegeEscalation is explicitly enabled, the setuid/setgid bits
// recorded in the snapshot are restored as-is.
func TestExport_FilePreservesSetuidWhenAllowed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "restore")
	exp := newExporter(t, root)
	exp.allowPrivilegeEscalation = true

	errs := run(t, exp, setuidRecord("/file", "content"))
	if errs[0] != nil {
		t.Fatalf("unexpected error: %v", errs[0])
	}

	fi, err := os.Stat(filepath.Join(root, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != os.ModeSetuid|os.ModeSetgid {
		t.Fatalf("setuid/setgid bits must be preserved when allowPrivilegeEscalation is enabled, got mode %v", fi.Mode())
	}
}

// TestExport_DirectoryStripsSetgidByDefault mirrors the file test for
// directories, which go through the dirPerms/permissions() path rather than
// writeAtomic.
func TestExport_DirectoryStripsSetgidByDefault(t *testing.T) {
	root := filepath.Join(t.TempDir(), "restore")
	exp := newExporter(t, root)

	dir := &connectors.Record{
		Pathname: "/dir",
		FileInfo: objects.FileInfo{Lname: "dir", Lmode: os.ModeDir | os.ModeSetgid | 0750},
	}
	errs := run(t, exp, dir)
	if errs[0] != nil {
		t.Fatalf("unexpected error: %v", errs[0])
	}

	fi, err := os.Stat(filepath.Join(root, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetgid != 0 {
		t.Fatalf("setgid bit must be stripped from directories by default, got mode %v", fi.Mode())
	}
}

func TestExport_DirectoryPreservesSetgidWhenAllowed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "restore")
	exp := newExporter(t, root)
	exp.allowPrivilegeEscalation = true

	dir := &connectors.Record{
		Pathname: "/dir",
		FileInfo: objects.FileInfo{Lname: "dir", Lmode: os.ModeDir | os.ModeSetgid | 0750},
	}
	errs := run(t, exp, dir)
	if errs[0] != nil {
		t.Fatalf("unexpected error: %v", errs[0])
	}

	fi, err := os.Stat(filepath.Join(root, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetgid == 0 {
		t.Fatalf("setgid bit must be preserved on directories when allowPrivilegeEscalation is enabled, got mode %v", fi.Mode())
	}
}

func TestExport_RestoreModeHelper(t *testing.T) {
	root := t.TempDir()
	exp := newExporter(t, root)

	fi := objects.FileInfo{Lmode: os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0644}

	exp.allowPrivilegeEscalation = false
	if got := exp.restoreMode(fi); got != 0644 {
		t.Errorf("restoreMode with allowPrivilegeEscalation=false = %v, want 0644 (special bits stripped)", got)
	}

	exp.allowPrivilegeEscalation = true
	want := os.FileMode(0644) | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if got := exp.restoreMode(fi); got != want {
		t.Errorf("restoreMode with allowPrivilegeEscalation=true = %v, want %v (special bits preserved)", got, want)
	}
}
