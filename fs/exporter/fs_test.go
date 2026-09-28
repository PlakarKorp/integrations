package exporter

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/require"
)

func newExporter(t *testing.T, root string) *FSExporter {
	t.Helper()
	return newExporterWithConfig(t, root, nil)
}

// newExporterWithConfig builds an FSExporter with additional config keys on
// top of "location", so the skip_* knobs introduced alongside this test
// suite can be exercised through NewFSExporter like a real caller would.
func newExporterWithConfig(t *testing.T, root string, extra map[string]string) *FSExporter {
	t.Helper()

	require.NoError(t, os.MkdirAll(root, 0700))

	config := map[string]string{"location": "fs://" + root}
	for k, v := range extra {
		config[k] = v
	}

	exp, err := NewFSExporter(t.Context(), &connectors.Options{MaxConcurrency: 1}, "fs", config)
	require.NoError(t, err)
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

	require.NoError(t, exp.Export(t.Context(), records, results))

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
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	canonPath := filepath.Join(dir, "canon")
	linkPath := filepath.Join(dir, "link")

	fi, err := os.Lstat(linkPath)
	require.NoError(t, err, "hardlink target was never created")
	require.Zero(t, fi.Mode()&os.ModeSymlink, "%s is a symlink, want a hardlink", linkPath)

	canonSt, err := os.Stat(canonPath)
	require.NoError(t, err)
	linkSt, err := os.Stat(linkPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(canonSt, linkSt), "%s and %s are not the same inode", canonPath, linkPath)
}

// A record whose pathname climbs out of the restore root must not write
// outside of it.
func TestExportRejectsDotDotPath(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "restore")

	exp := newExporter(t, root)
	run(t, exp, fileRecord("/../escaped", "owned"))

	_, err := os.Lstat(filepath.Join(base, "escaped"))
	require.True(t, os.IsNotExist(err), "record escaped the restore root: %v", err)
}

// The core regression: a symlink from the archive pointing outside the root,
// followed by a record that writes *through* it.  The lexical containment
// check that used to guard this could not see the second path leaving.
func TestExportDoesNotWriteThroughSymlink(t *testing.T) {
	// not enough permissions to create symlinks.
	if runtime.GOOS == "windows" {
		t.Skip()
	}

	base := t.TempDir()
	root := filepath.Join(base, "restore")

	outside := filepath.Join(base, "outside")
	require.NoError(t, os.Mkdir(outside, 0700))

	exp := newExporter(t, root)
	errs := run(t, exp,
		symlinkRecord("/link", outside),
		fileRecord("/link/pwned", "owned"),
	)

	// The symlink itself is restored faithfully -- that is the snapshot's
	// content -- but writing through it has to fail.
	require.NoError(t, errs[0], "symlink should still be restored verbatim")
	require.Error(t, errs[1], "writing through an escaping symlink was allowed")

	_, err := os.Lstat(filepath.Join(outside, "pwned"))
	require.True(t, os.IsNotExist(err), "wrote through the symlink into %s: %v", outside, err)
}

// Same shape, but the symlink is absolute and the write reaches it through a
// deeper path.
func TestExportDoesNotWriteThroughNestedSymlink(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "restore")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "etc"), 0700))

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

	require.Error(t, errs[2], "writing through a nested escaping symlink was allowed")
	_, err := os.Lstat(filepath.Join(outside, "etc", "pwned"))
	require.True(t, os.IsNotExist(err), "wrote through the nested symlink: %v", err)
}

// Restoring mtimes on a symlink that escapes the root must not touch
// whatever it points at.
func TestSymlinkLutimesDoesNotTouchEscapeTarget(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "restore")

	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("shh"), 0600); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(secret, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(secret)
	if err != nil {
		t.Fatal(err)
	}

	exp := newExporter(t, root)
	rec := symlinkRecord("/link", secret)
	rec.FileInfo.LmodTime = time.Now().Add(-time.Hour).Truncate(time.Second)
	errs := run(t, exp, rec)

	if errs[0] != nil {
		t.Fatalf("symlink should still be restored verbatim: %v", errs[0])
	}

	after, err := os.Stat(secret)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("restoring the symlink's mtime touched its target: before=%v after=%v", before.ModTime(), after.ModTime())
	}

	linkSt, err := os.Lstat(filepath.Join(root, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := linkSt.ModTime().Sub(rec.FileInfo.LmodTime); diff < -2*time.Second || diff > 2*time.Second {
		t.Errorf("symlink mtime = %v, want ~%v", linkSt.ModTime(), rec.FileInfo.LmodTime)
	}
}

// Ordinary restores keep working: nested dirs, file contents, and a symlink
// that stays inside the root.
func TestExportRestoresNormalTree(t *testing.T) {
	// not enough permissions to create symlinks.
	if runtime.GOOS == "windows" {
		t.Skip()
	}

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
		require.NoError(t, err, "record %d failed", i)
	}

	got, err := os.ReadFile(filepath.Join(root, "dir", "hello"))
	require.NoError(t, err)
	require.Equal(t, "world", string(got))

	target, err := os.Readlink(filepath.Join(root, "dir", "rel"))
	require.NoError(t, err)
	require.Equal(t, "hello", target)
}

func TestRelativeRejectsEscapes(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{name: "regular path", in: "/a/b", want: filepath.Join("a", "b"), ok: true},
		{name: "root", in: "/", want: ".", ok: true},
		{name: "empty path", in: "", ok: false},
		{name: "leading double dot escape", in: "/../../etc/passwd", ok: false},
		{name: "mid-path double dot escape", in: "/a/../../b", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := relative(tc.in)
			if tc.ok {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// setuidRecord returns a file record whose recorded mode carries the
// setuid, setgid and sticky bits, so permissions()/skip_permissions can be
// exercised against it.
func setuidRecord(pathname, content string) *connectors.Record {
	return &connectors.Record{
		Pathname: pathname,
		Reader:   io.NopCloser(strings.NewReader(content)),
		FileInfo: objects.FileInfo{Lname: filepath.Base(pathname), Lmode: os.ModeSetuid | os.ModeSetgid | 0755, Lsize: int64(len(content))},
	}
}

// TestExport_SkipPermissions covers the skip_permissions knob against both
// the writeAtomic (file) and dirPerms (directory) paths into permissions():
// by default the special bits recorded in the snapshot are preserved, and
// with skip_permissions set, chmod never runs so the entry keeps whatever
// mode it was created with.
func TestExport_SkipPermissions(t *testing.T) {
	// Windows has no setuid/setgid bits, and os.Stat there synthesizes mode
	// from the read-only attribute alone, so chmod'd values aren't observable.
	if runtime.GOOS == "windows" {
		t.Skip()
	}

	cases := []struct {
		name            string
		isDir           bool
		skipPermissions bool
	}{
		{name: "file preserves setuid/setgid by default", isDir: false, skipPermissions: false},
		{name: "file skips chmod when skip_permissions is set", isDir: false, skipPermissions: true},
		{name: "directory preserves setgid by default", isDir: true, skipPermissions: false},
		{name: "directory skips chmod when skip_permissions is set", isDir: true, skipPermissions: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "restore")

			var exp *FSExporter
			if tc.skipPermissions {
				exp = newExporterWithConfig(t, root, map[string]string{"skip_permissions": "true"})
			} else {
				exp = newExporter(t, root)
			}

			entry := "file"
			rec := setuidRecord("/file", "content")
			specialBits := os.ModeSetuid | os.ModeSetgid
			if tc.isDir {
				entry = "dir"
				rec = &connectors.Record{
					Pathname: "/dir",
					FileInfo: objects.FileInfo{Lname: "dir", Lmode: os.ModeDir | os.ModeSetgid | 0750},
				}
				specialBits = os.ModeSetgid
			}

			errs := run(t, exp, rec)
			require.NoError(t, errs[0])

			fi, err := os.Stat(filepath.Join(root, entry))
			require.NoError(t, err)

			require.Equal(t, !tc.skipPermissions, fi.Mode()&specialBits != 0,
				"special bits present, mode %v", fi.Mode())

			if tc.isDir {
				if tc.skipPermissions {
					require.Equal(t, os.FileMode(0700), fi.Mode().Perm(),
						"directory mode must stay at the Mkdir default when skip_permissions is set")
				}
			} else {
				require.Equal(t, !tc.skipPermissions, fi.Mode().Perm() == 0755,
					"file permission bits %v, recorded 0755 applied", fi.Mode().Perm())
			}
		})
	}
}

// TestExport_SkipTimes verifies that, by default, a restored file's mtime
// matches the one recorded in the snapshot, and that skip_times leaves it
// at whatever the restore process gave it instead.
func TestExport_SkipTimes(t *testing.T) {
	recorded := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)

	cases := []struct {
		name      string
		skipTimes bool
	}{
		{name: "restores recorded mtime by default", skipTimes: false},
		{name: "leaves mtime untouched when skip_times is set", skipTimes: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "restore")

			var exp *FSExporter
			if tc.skipTimes {
				exp = newExporterWithConfig(t, root, map[string]string{"skip_times": "true"})
			} else {
				exp = newExporter(t, root)
			}

			rec := fileRecord("/file", "content")
			rec.FileInfo.LmodTime = recorded

			before := time.Now()
			errs := run(t, exp, rec)
			require.NoError(t, errs[0])

			fi, err := os.Stat(filepath.Join(root, "file"))
			require.NoError(t, err)

			require.Equal(t, !tc.skipTimes, fi.ModTime().Equal(recorded),
				"mtime set to recorded value, mtime %v", fi.ModTime())
			if tc.skipTimes {
				// Filesystem mtime granularity can trail time.Now() by a
				// handful of microseconds, so allow slack rather than
				// asserting a strict happens-after relationship.
				require.False(t, fi.ModTime().Before(before.Add(-time.Second)),
					"mtime %v, want a value no earlier than %v", fi.ModTime(), before)
			}
		})
	}
}

// TestExport_SkipOwnership verifies that, when running as root, a restored
// file's ownership matches the one recorded in the snapshot by default, and
// that skip_ownership leaves it owned by the restoring process instead.
// It requires root, since permissions() only attempts chown as root.
func TestExport_SkipOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise chown")
	}

	const wantUid, wantGid = 1, 1

	cases := []struct {
		name          string
		skipOwnership bool
	}{
		{name: "restores recorded ownership by default", skipOwnership: false},
		{name: "leaves ownership untouched when skip_ownership is set", skipOwnership: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "restore")

			var exp *FSExporter
			if tc.skipOwnership {
				exp = newExporterWithConfig(t, root, map[string]string{"skip_ownership": "true"})
			} else {
				exp = newExporter(t, root)
			}

			rec := fileRecord("/file", "content")
			rec.FileInfo.Luid = wantUid
			rec.FileInfo.Lgid = wantGid

			errs := run(t, exp, rec)
			require.NoError(t, errs[0])

			fi, err := os.Stat(filepath.Join(root, "file"))
			require.NoError(t, err)
			uid, gid := fileOwner(t, fi)

			gotRecorded := uid == wantUid && gid == wantGid
			require.Equal(t, !tc.skipOwnership, gotRecorded,
				"ownership set to recorded value, uid=%d gid=%d", uid, gid)
		})
	}
}

// TestExport_SkipRootPermsAndTime verifies that skip_root_perms_and_time
// only bypasses permissions()/mtime restore for the restore root itself,
// leaving regular subdirectories unaffected.
func TestExport_SkipRootPermsAndTime(t *testing.T) {
	// Windows has no setuid/setgid bits, and os.Stat there synthesizes mode
	// from the read-only attribute alone, so chmod'd values aren't observable.
	if runtime.GOOS == "windows" {
		t.Skip()
	}

	recorded := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)

	cases := []struct {
		name                 string
		skipRootPermsAndTime bool
	}{
		{name: "restores recorded root perms and mtime by default", skipRootPermsAndTime: false},
		{name: "leaves root perms and mtime untouched when set", skipRootPermsAndTime: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "restore")

			var exp *FSExporter
			if tc.skipRootPermsAndTime {
				exp = newExporterWithConfig(t, root, map[string]string{"skip_root_perms_and_time": "true"})
			} else {
				exp = newExporter(t, root)
			}

			before := time.Now()

			rootDir := &connectors.Record{
				Pathname: "/",
				FileInfo: objects.FileInfo{Lname: "/", Lmode: os.ModeDir | 0750, LmodTime: recorded},
			}
			subDir := &connectors.Record{
				Pathname: "/sub",
				FileInfo: objects.FileInfo{Lname: "sub", Lmode: os.ModeDir | 0750, LmodTime: recorded},
			}
			errs := run(t, exp, rootDir, subDir)
			for _, err := range errs {
				require.NoError(t, err)
			}

			rootFi, err := os.Stat(root)
			require.NoError(t, err)

			gotRootRestored := rootFi.Mode().Perm() == 0750 && rootFi.ModTime().Equal(recorded)
			require.Equal(t, !tc.skipRootPermsAndTime, gotRootRestored,
				"root perms/mtime restored, mode %v, mtime %v", rootFi.Mode().Perm(), rootFi.ModTime())
			if tc.skipRootPermsAndTime {
				require.Equal(t, os.FileMode(0700), rootFi.Mode().Perm(),
					"restore root mode must stay at the Mkdir default")
				// Filesystem mtime granularity can trail time.Now() by a
				// handful of microseconds, so allow slack rather than
				// asserting a strict happens-after relationship.
				require.False(t, rootFi.ModTime().Before(before.Add(-time.Second)),
					"restore root mtime %v, want a value no earlier than %v", rootFi.ModTime(), before)
			}

			// A regular subdirectory must always be restored, regardless of
			// skip_root_perms_and_time.
			subFi, err := os.Stat(filepath.Join(root, "sub"))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0750), subFi.Mode().Perm(), "subdirectory mode must still be restored")
			require.True(t, subFi.ModTime().Equal(recorded), "subdirectory mtime must still be restored: got %v, want %v", subFi.ModTime(), recorded)
		})
	}
}
