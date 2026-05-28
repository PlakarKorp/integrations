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
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
)

// drainImporter runs imp.Import to completion and returns the set of pathnames
// (excluding errored records) that the importer surfaced as records.
func drainImporter(t *testing.T, imp *FSImporter) map[string]bool {
	t.Helper()

	records := make(chan *connectors.Record, 64)
	results := make(chan *connectors.Result, 64)

	// connectors.Record.Ok() sends back on results; we don't care about the
	// content here, just drain it so Import can complete without blocking.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range results {
		}
	}()

	out := map[string]bool{}
	importDone := make(chan error, 1)
	go func() {
		importDone <- imp.Import(context.Background(), records, results)
	}()

	for rec := range records {
		if rec.Err == nil {
			out[rec.Pathname] = true
		}
		results <- rec.Ok()
	}
	close(results)
	<-done

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
	indexPath := filepath.Join(frontend, "index.html")
	if !got[indexPath] {
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
		if got[p] {
			t.Errorf("expected %s to be excluded by '**/frontend/*'; but it was included", p)
		}
	}

	// Sanity: README.md outside frontend/ should be included.
	readme := filepath.Join(root, "ui", "README.md")
	if !got[readme] {
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
		p := filepath.Join(root, name)
		if !got[p] {
			t.Errorf("sibling %s of an excluded file was incorrectly dropped", p)
		}
	}
	if got[filepath.Join(root, "a.log")] {
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

	if got[filepath.Join(nm, "index.js")] {
		t.Errorf("descended into node_modules/ even though it is excluded")
	}
	if !got[filepath.Join(root, "main.go")] {
		t.Errorf("main.go (outside node_modules/) should be included")
	}
}
