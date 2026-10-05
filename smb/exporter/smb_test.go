/*
 * Copyright (c) 2025 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package exporter

import (
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
)

func TestSafe(t *testing.T) {
	tests := []struct {
		name     string
		rootDir  string
		pathname string
		want     bool
	}{
		// --- rootDir "/" (share root): every absolute path is contained ---
		{"root-slash exact match", "/", "/", true},
		{"root-slash top-level child", "/", "/toto", true},
		{"root-slash nested child", "/", "/toto/titi.txt", true},

		// --- normal, non-root path ---
		{"exact match", "/repo", "/repo", true},
		{"top-level child", "/repo", "/repo/toto", true},
		{"nested child", "/repo", "/repo/toto/titi.txt", true},
		{"child with duplicated slash separator", "/repo", "/repo//toto", true},

		// --- sibling / prefix confusion (the classic path-traversal-style bug) ---
		{"sibling with same string prefix, longer", "/repo", "/repox", false},
		{"sibling with same string prefix, longer dir", "/repo", "/repoo/toto", false},
		{"sibling with same string prefix, shorter", "/repo", "/rep", false},
		{"unrelated absolute path", "/repo", "/other", false},
		{"parent of root", "/repo", "/", false},
		{"grandparent of root", "/repo/sub", "/repo", false},

		// --- unclean pathname, e.g. from a hostile "../" record still under
		// the joined-and-cleaned prefix ---
		{"pathname with dot-dot resolving back inside root", "/repo", "/repo/a/../b", true},
		{"pathname with dot-dot resolving outside root", "/repo", "/repo/../outside", false},

		// --- deeply nested paths ---
		{"deeply nested child", "/a/b/c", "/a/b/c/d/e/f/g.txt", true},
		{"deeply nested unrelated sibling", "/a/b/c", "/a/b/cc/d/e/f/g.txt", false},

		// --- unclean rootDir: safe() must clean rootDir too, not just pathname ---
		{"root with dot segment", "/repo/./sub", "/repo/sub", true},
		{"root with double slashes", "/repo//sub", "/repo/sub/x", true},
		{"root with dot-dot collapsing to parent", "/repo/sub/..", "/repo/x", true},
		{"root trailing slash, exact match", "/repo/", "/repo", true},
		{"root trailing slash, child", "/repo/", "/repo/toto", true},
		{"root trailing slash, sibling rejected", "/repo/", "/repox", false},

		// --- empty/malformed rootDir must never authorize escape ---
		{"empty root, absolute pathname", "", "/foo", false},
		{"empty root and empty pathname", "", "", true},
		{"relative root, absolute pathname", "repo", "/repo", false},
		{"absolute root, relative pathname", "/repo", "repo", false},
		{"dot root, absolute pathname", ".", "/foo", false},

		// --- case sensitivity: comparison is literal, not case-insensitive ---
		{"case mismatch rejected", "/Repo", "/repo/x", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Exporter{rootDir: tt.rootDir}
			if got := p.safe(tt.pathname); got != tt.want {
				t.Errorf("safe(%q) with rootDir %q = %v; want %v", tt.pathname, tt.rootDir, got, tt.want)
			}
		})
	}
}

func TestExportOne_RejectsPathEscapingRoot(t *testing.T) {
	p := &Exporter{rootDir: "/repo"}

	err := p.exportOne(&connectors.Record{Pathname: "/../../outside.txt"})
	if err == nil {
		t.Fatal("expected exportOne to reject a pathname escaping the restore root")
	}
}
