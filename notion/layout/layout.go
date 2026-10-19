// Package layout names the files a Notion backup is made of. The importer
// writes this tree and the exporter reads it back, so it lives in one place.
//
//	/workspace.json                 roots of the backup, with titles
//	/pages/<id>/page.json           the page object
//	/pages/<id>/blocks.json         its blocks, children nested under "children"
//	/pages/<id>/files/<block-id>.*  files Notion hosted for its blocks
//	/pages/<id>/files/icon.*        a hosted page icon, cover likewise
//	/pages/<id>/pages/<id>/...      child pages
//	/pages/<id>/databases/<id>/...  child databases
//	/databases/<id>/database.json   the database object, with its schema
//	/databases/<id>/pages/<id>/...  its rows, each a page
//
// Directories are named by id, not title: titles change, collide and carry
// slashes, and a renamed page should not look like a new one to the
// deduplicating store.
package layout

import (
	"path"
	"strings"
)

const (
	// WorkspaceFile lists the roots of the backup.
	WorkspaceFile = "workspace.json"
	// PageFile holds a page object.
	PageFile = "page.json"
	// BlocksFile holds the block tree of a page.
	BlocksFile = "blocks.json"
	// DatabaseFile holds a database object.
	DatabaseFile = "database.json"
	// FilesDir holds the hosted files of a page or database.
	FilesDir = "files"
	// PagesDir holds the child pages, or rows, of its parent.
	PagesDir = "pages"
	// DatabasesDir holds the child databases of a page.
	DatabasesDir = "databases"
	// IconFile and CoverFile, in FilesDir, hold a hosted icon or cover,
	// with the extension of the original name when it had one.
	IconFile  = "icon"
	CoverFile = "cover"
)

// Workspace is the content of WorkspaceFile.
type Workspace struct {
	Roots []Root `json:"roots"`
}

// Root is one top-level page or database of the backup.
type Root struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Title  string `json:"title"`
	Path   string `json:"path"`
}

// PageDir is the directory of a page under parent, "" for a root.
func PageDir(parent, id string) string { return path.Join("/", parent, PagesDir, id) }

// DatabaseDir is the directory of a database under parent, "" for a root.
func DatabaseDir(parent, id string) string { return path.Join("/", parent, DatabasesDir, id) }

// FileName is the name, under FilesDir, of a file hosted for the given
// block, icon or cover. The extension comes from the download URL so the
// file opens with the right tool once restored elsewhere.
func FileName(base, rawurl string) string {
	return base + Extension(rawurl)
}

// Extension is the extension of the last path segment of a URL, query and
// fragment stripped, empty when it has none or it is not a plain one.
func Extension(rawurl string) string {
	u := rawurl
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	ext := path.Ext(u)
	if len(ext) < 2 || len(ext) > 16 || strings.ContainsAny(ext, "/\\") {
		return ""
	}
	for _, r := range ext[1:] {
		if !isAlnum(r) {
			return ""
		}
	}
	return strings.ToLower(ext)
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// StripExtension is the base FileName was built from.
func StripExtension(name string) string {
	return strings.TrimSuffix(name, path.Ext(name))
}
