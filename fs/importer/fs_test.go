/*
 * Copyright (c) 2026 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 */

package importer

import (
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
)

// drainImporter runs imp.Import to completion and returns the set of pathnames
// (excluding errored records) that the importer surfaced as records.
func drainImporter(t *testing.T, imp *FSImporter) map[string]struct{} {
	t.Helper()

	out := map[string]struct{}{}
	for _, rec := range drainRecords(t, imp) {
		out[rec.Pathname] = struct{}{}
	}
	return out
}

// drainRecords runs imp.Import to completion and returns every record that
// is not an error.
func drainRecords(t *testing.T, imp *FSImporter) []*connectors.Record {
	t.Helper()

	records := make(chan *connectors.Record, 64)

	importDone := make(chan error, 1)
	go func() {
		importDone <- imp.Import(context.Background(), records, nil)
	}()

	var out []*connectors.Record
	for rec := range records {
		if rec.Err == nil {
			out = append(out, rec)
		}
	}

	if err := <-importDone; err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	return out
}

// newImporter builds an FSImporter for a temp root with the supplied -ignore
// patterns.
func newImporter(t *testing.T, root string, excludes []string) *FSImporter {
	t.Helper()
	opts := &connectors.Options{
		Hostname:       "test",
		MaxConcurrency: 2,
		Excludes:       excludes,
	}
	imp, err := NewFSImporter(context.Background(), opts, "fs", map[string]string{
		"location": "fs://" + root,
	})
	if err != nil {
		t.Fatalf("NewFSImporter: %v", err)
	}
	return imp.(*FSImporter)
}

// TestImporter_IgnoreNegation_PlakarKorpPlakar2120 is the regression test for
// https://github.com/PlakarKorp/plakar/issues/2120
//
// Patterns:
//
//	-ignore '**/frontend/*'
//	-ignore '!**/frontend/index.html'
//
// Expected: every file inside frontend/ is excluded *except* index.html.
// Before the fix, a non-directory matching the first rule returned
// filepath.SkipDir, which silently dropped every sibling that walkDir hadn't
// yet visited — including index.html when its name sorted after some other
// excluded sibling.
func TestImporter_IgnoreNegation_PlakarKorpPlakar2120(t *testing.T) {
	root := t.TempDir()

	frontend := filepath.Join(root, "ui", "v2", "frontend")
	if err := os.MkdirAll(frontend, 0755); err != nil {
		t.Fatal(err)
	}
	// File names chosen so that "index.html" sorts after several sibling
	// files. This guarantees that, with the old buggy SkipDir behavior, the
	// walker would skip index.html. With the fix it must be kept.
	files := []string{
		"app.js",
		"bundle.css",
		"data.json",
		"helper.go",
		"index.html",
		"sub.html",
		"vendor.min.js",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(frontend, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// A file outside frontend/ to make sure the walk continues.
	if err := os.WriteFile(filepath.Join(root, "ui", "README.md"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	imp := newImporter(t, root, []string{
		"**/frontend/*",
		"!**/frontend/index.html",
	})

	got := drainImporter(t, imp)

	// index.html must be in the output (re-included by the negation rule).
	indexPath := path.Join(toslash(frontend), "index.html")
	if _, ok := got[indexPath]; !ok {
		var paths []string
		for p := range got {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		t.Fatalf("expected %s to be present (re-included by negation rule); got:\n  %s",
			indexPath, strings.Join(paths, "\n  "))
	}

	// All other files in frontend/ must NOT be in the output.
	for _, name := range files {
		if name == "index.html" {
			continue
		}
		p := filepath.Join(frontend, name)
		if _, ok := got[p]; ok {
			t.Errorf("expected %s to be excluded by '**/frontend/*'; but it was included", p)
		}
	}

	// Sanity: README.md outside frontend/ should be included.
	readme := path.Join(toslash(root), "ui", "README.md")
	if _, ok := got[readme]; !ok {
		t.Errorf("expected %s (outside frontend/) to be included", readme)
	}
}

// TestImporter_ExcludedFileDoesNotSkipSiblings asserts the narrower property
// directly: a non-directory matching an ignore rule must not cause walkDir to
// drop later siblings in the same directory.
func TestImporter_ExcludedFileDoesNotSkipSiblings(t *testing.T) {
	root := t.TempDir()

	// "a.log" sorts before "b.txt", "c.txt", etc.
	for _, name := range []string{"a.log", "b.txt", "c.txt", "d.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	imp := newImporter(t, root, []string{"*.log"})
	got := drainImporter(t, imp)

	for _, name := range []string{"b.txt", "c.txt", "d.txt"} {
		p := path.Join(toslash(root), name)
		if _, ok := got[p]; !ok {
			t.Errorf("sibling %s of an excluded file was incorrectly dropped", p)
		}
	}
	if _, ok := got[filepath.Join(root, "a.log")]; ok {
		t.Errorf("a.log should have been excluded")
	}
}

// TestImporter_ExcludedDirectoryPrunesSubtree asserts the optimization we want
// to keep: when an ignore rule matches a directory, we don't descend into it.
func TestImporter_ExcludedDirectoryPrunesSubtree(t *testing.T) {
	root := t.TempDir()

	nm := filepath.Join(root, "node_modules", "react", "src")
	if err := os.MkdirAll(nm, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nm, "index.js"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	imp := newImporter(t, root, []string{"node_modules/"})
	got := drainImporter(t, imp)

	if _, ok := got[path.Join(toslash(nm), "index.js")]; ok {
		t.Errorf("descended into node_modules/ even though it is excluded")
	}
	if _, ok := got[path.Join(toslash(root), "main.go")]; !ok {
		t.Errorf("main.go (outside node_modules/) should be included")
	}
}

func TestToSlash(t *testing.T) {
	// backslash is only a separator on windows
	if runtime.GOOS != "windows" {
		t.Skip()
	}

	suite := []struct {
		t string
		e string
	}{
		{"C:\\test", "/C/test"},
		{"C:\\", "/C"},
		{"\\\\host\\share", "/host/share"},
		{"\\\\host\\share\\file.mp3", "/host/share/file.mp3"},
	}

	for _, test := range suite {
		if r := toslash(test.t); r != test.e {
			t.Errorf("toslash(%s) yield %s; expected %s",
				test.t, r, test.e)
		}
	}
}

func TestImporter_VolumeRootName(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("volume roots only exist on windows")
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var drive string
	for c := 'Z'; c >= 'D'; c-- {
		if _, err := os.Lstat(string(c) + `:\`); err != nil {
			drive = string(c) + ":"
			break
		}
	}
	if drive == "" {
		t.Skip("no free drive letter")
	}
	if out, err := exec.Command("subst", drive, root).CombinedOutput(); err != nil {
		t.Skipf("subst %s %s: %v: %s", drive, root, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("subst", drive, "/D").Run() })

	rootpath := toslash(drive + `\`)
	want := path.Base(rootpath)

	seen := 0
	for _, rec := range drainRecords(t, newImporter(t, drive+`\`, nil)) {
		if rec.Pathname != rootpath {
			continue
		}
		seen++
		if rec.FileInfo.Lname != want {
			t.Errorf("record %q named %q; expected %q", rec.Pathname, rec.FileInfo.Lname, want)
		}
	}
	if seen == 0 {
		t.Errorf("no record for %q", rootpath)
	}
}
