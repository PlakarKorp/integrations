/*
 * Copyright (c) 2026 Gilles Chehade <gilles@poolp.org>
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

// Package layout defines how a Notion workspace is laid out in a
// snapshot.  The tree mirrors Notion's own hierarchy, directories are
// named after the object they hold, and the files inside an object's
// directory are named so that a lexical walk yields them in the order a
// restore needs them: the object itself, its files, its blocks, its
// comments, then its children.
//
//	/workspace.json                    the bot user and its workspace
//	/users.json                        the workspace members
//	/pages/<Title id>/                 a top-level page
//	    about.json                     the page object
//	    assets/<file id>/<name>        files hosted by Notion
//	    blocks.json                    the block tree
//	    comments.json                  the open comments
//	    content.md                     the page as Notion-flavored markdown
//	    databases/<Title id>/          child databases
//	    pages/<Title id>/              child pages
//	/databases/<Title id>/             a top-level database
//	    about.json                     the database object
//	    assets/...                     icon and cover
//	    pages/<Title id>/              pages parented by the database itself
//	    sources/<Title id>/            its data sources
//	        about.json                 the data source object, with the schema
//	        rows/<Title id>/           its rows, laid out as pages
//	    views.json                     its views
package layout

import (
	"path"
	"strings"
	"unicode"

	"github.com/PlakarKorp/integrations/notion/api"
)

const (
	AboutFile    = "about.json"
	AssetsDir    = "assets"
	BlocksFile   = "blocks.json"
	CommentsFile = "comments.json"
	ContentFile  = "content.md"
	ViewsFile    = "views.json"

	DatabasesDir = "databases"
	PagesDir     = "pages"
	SourcesDir   = "sources"
	RowsDir      = "rows"

	WorkspaceFile = "workspace.json"
	UsersFile     = "users.json"

	maxTitleRunes = 80
)

// Kind says what a snapshot path is.
type Kind int

const (
	KindUnknown Kind = iota
	KindRoot
	KindRootFile  // workspace.json, users.json
	KindContainer // pages/, databases/, sources/, rows/
	KindEntity    // the directory of a page, database or data source
	KindAbout
	KindAssetsDir
	KindAsset
	KindBlocks
	KindComments
	KindContent
	KindViews
)

// Entity is the kind of object a directory holds.
type Entity string

const (
	EntityNone       Entity = ""
	EntityPage       Entity = "page"
	EntityDatabase   Entity = "database"
	EntityDataSource Entity = "data_source"
)

// Path is what a snapshot path means.
type Path struct {
	Kind Kind
	// Entity is the directory of the object the path belongs to,
	// Object its kind and ID its Notion id.
	Entity    string
	Object    Entity
	ID        string
	ParentDir string // the entity directory enclosing Entity, "" at the top
	AssetKey  string // "<file id>/<name>" for assets
}

// DirName names the directory of an object: its title, made safe for a
// path and cut short, followed by its id without dashes, which keeps
// names unique and lets the restore find the id back.
func DirName(title, id string) string {
	t := []rune(strings.TrimSpace(sanitize(title)))
	if len(t) > maxTitleRunes {
		t = t[:maxTitleRunes]
	}
	s := strings.TrimSpace(string(t))
	if s == "" {
		s = "Untitled"
	}
	return s + " " + api.CompactID(id)
}

// ParseDirName gets the id back out of DirName.
func ParseDirName(name string) (string, bool) {
	i := strings.LastIndexByte(name, ' ')
	if i < 0 {
		return "", false
	}
	id := name[i+1:]
	if len(id) != 32 || !api.IsID(id) {
		return "", false
	}
	return api.ExpandID(id), true
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':':
			b.WriteRune('-')
		case unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	// a leading dot hides the entry on most tools
	if strings.HasPrefix(out, ".") {
		out = "_" + out[1:]
	}
	return out
}

// childKind says what a container holds under an object of kind parent.
func childKind(parent Entity, container string) (Entity, bool) {
	switch parent {
	case EntityNone, EntityPage:
		switch container {
		case PagesDir:
			return EntityPage, true
		case DatabasesDir:
			return EntityDatabase, true
		}
	case EntityDatabase:
		switch container {
		case SourcesDir:
			return EntityDataSource, true
		case PagesDir:
			return EntityPage, true
		}
	case EntityDataSource:
		if container == RowsDir {
			return EntityPage, true
		}
	default:
	}
	return EntityNone, false
}

// ContainerFor is the directory a child object goes under.
func ContainerFor(parentDir string, parent, child Entity) string {
	switch child {
	case EntityPage:
		if parent == EntityDataSource {
			return path.Join("/", parentDir, RowsDir)
		}
		return path.Join("/", parentDir, PagesDir)
	case EntityDatabase:
		return path.Join("/", parentDir, DatabasesDir)
	case EntityDataSource:
		return path.Join("/", parentDir, SourcesDir)
	default:
		return ""
	}
}

// Classify says what an absolute snapshot path is.
func Classify(p string) Path {
	p = path.Clean("/" + p)
	if p == "/" {
		return Path{Kind: KindRoot}
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	unknown := Path{Kind: KindUnknown}

	var st Path
	for i := 0; i < len(segs); {
		seg := segs[i]
		last := i == len(segs)-1
		cur := "/" + strings.Join(segs[:i+1], "/")

		switch seg {
		case PagesDir, DatabasesDir, SourcesDir, RowsDir:
			child, ok := childKind(st.Object, seg)
			if !ok {
				return unknown
			}
			if last {
				st.Kind = KindContainer
				return st
			}
			id, ok := ParseDirName(segs[i+1])
			if !ok {
				return unknown
			}
			st = Path{Entity: cur + "/" + segs[i+1], Object: child, ID: id, ParentDir: st.Entity}
			if i+1 == len(segs)-1 {
				st.Kind = KindEntity
				return st
			}
			i += 2
			continue

		case WorkspaceFile, UsersFile:
			if i == 0 && last {
				return Path{Kind: KindRootFile}
			}
			return unknown
		}

		if st.Entity == "" {
			return unknown
		}
		switch seg {
		case AboutFile:
			if last {
				st.Kind = KindAbout
				return st
			}
		case BlocksFile, CommentsFile, ContentFile:
			if last && st.Object == EntityPage {
				st.Kind = map[string]Kind{BlocksFile: KindBlocks, CommentsFile: KindComments, ContentFile: KindContent}[seg]
				return st
			}
		case ViewsFile:
			if last && st.Object == EntityDatabase {
				st.Kind = KindViews
				return st
			}
		case AssetsDir:
			if st.Object == EntityDataSource {
				return unknown
			}
			if last {
				st.Kind = KindAssetsDir
				return st
			}
			rest := segs[i+1:]
			switch {
			case len(rest) == 1 && rest[0] != "":
				// the directory named after the file id
				st.Kind = KindAssetsDir
				return st
			case len(rest) == 2 && rest[0] != "" && rest[1] != "":
				st.Kind = KindAsset
				st.AssetKey = rest[0] + "/" + rest[1]
				return st
			}
			return unknown
		}
		return unknown
	}
	return unknown
}
