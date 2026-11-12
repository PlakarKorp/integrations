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

package fakenotion

import (
	"bytes"
	"fmt"
)

// Seeded names the objects Seed created.
type Seeded struct {
	Alice, Bob string

	Engineering string // the top page
	ChildA      string
	ChildB      string
	Nested      string // a page inside a toggle
	Orphan      string // a page whose parent the token cannot see
	Trashed     string

	Paragraph string // a block with a comment
	Image     string
	Toggle    string
	Synced    string

	Projects, ProjectsDS string
	Tasks, TasksDS       string
	ProjectRow           string
	MoreProjects         []string
	Wiki                 string // a page parented by the Projects database
	TaskRows             []string

	Cover, Diagram, Attachment, Spec                 map[string]any
	CoverData, DiagramData, AttachmentData, SpecData []byte

	ViewAll, ViewBoard string
}

// Seed fills a workspace with a bit of everything: nested pages,
// every kind of block the restore handles specially, a database with
// a relation to another one, rows with files, comments with an
// attachment, views, an orphan and a page in the trash.
func Seed(s *Server) *Seeded {
	d := &Seeded{}
	d.Alice = s.AddUser("Alice", "alice@example.com")
	d.Bob = s.AddUser("Bob", "bob@example.com")

	d.CoverData = bytes.Repeat([]byte("cover"), 100)
	d.DiagramData = bytes.Repeat([]byte("diagram"), 1000)
	d.AttachmentData = []byte("attachment bytes")
	d.SpecData = []byte("%PDF-1.4 spec")
	d.Cover = s.AddFile("cover.png", "image/png", d.CoverData)
	d.Diagram = s.AddFile("diagram.png", "image/png", d.DiagramData)
	d.Attachment = s.AddFile("notes.txt", "text/plain", d.AttachmentData)
	d.Spec = s.AddFile("spec.pdf", "application/pdf", d.SpecData)

	d.Engineering = s.AddPage("workspace", "", "Engineering", nil)
	s.SetIcon(d.Engineering, map[string]any{"type": "emoji", "emoji": "🛠"})
	s.SetCover(d.Engineering, d.Cover)

	s.AddBlock(d.Engineering, "heading_1", map[string]any{"rich_text": Text("Welcome"), "color": "default", "is_toggleable": false})
	// the API returns null for a paragraph's unset icon
	d.Paragraph = s.AddBlock(d.Engineering, "paragraph", map[string]any{"rich_text": Text("Hello world"), "color": "default", "icon": nil})
	d.Image = s.AddBlock(d.Engineering, "image", map[string]any{"type": "file", "file": d.Diagram["file"], "caption": Text("the diagram")})
	d.ChildA = s.AddPage("page_id", d.Engineering, "Child A", nil)
	s.AddBlock(d.ChildA, "paragraph", map[string]any{"rich_text": Text("inside child a"), "color": "default"})
	d.Toggle = s.AddBlock(d.Engineering, "toggle", map[string]any{"rich_text": Text("Details"), "color": "default"})
	s.AddBlock(d.Toggle, "paragraph", map[string]any{"rich_text": Text("inside toggle"), "color": "default"})
	d.Nested = s.AddPage("page_id", d.Toggle, "Nested", nil)
	cl := s.AddBlock(d.Engineering, "column_list", map[string]any{})
	c1 := s.AddBlock(cl, "column", map[string]any{"width_ratio": 0.5})
	c2 := s.AddBlock(cl, "column", map[string]any{"width_ratio": 0.5})
	s.AddBlock(c1, "paragraph", map[string]any{"rich_text": Text("left"), "color": "default"})
	ct := s.AddBlock(c1, "table", map[string]any{"table_width": float64(1), "has_column_header": false, "has_row_header": false})
	s.AddBlock(ct, "table_row", map[string]any{"cells": []any{Text("in a column")}})
	bl := s.AddBlock(c2, "bulleted_list_item", map[string]any{"rich_text": Text("right"), "color": "default"})
	s.AddBlock(bl, "paragraph", map[string]any{"rich_text": Text("deep in a column"), "color": "default"})
	tb := s.AddBlock(d.Engineering, "table", map[string]any{"table_width": float64(2), "has_column_header": true, "has_row_header": false})
	s.AddBlock(tb, "table_row", map[string]any{"cells": []any{Text("a"), Text("b")}})
	s.AddBlock(tb, "table_row", map[string]any{"cells": []any{Text("c"), Text("d")}})
	s.AddBlock(d.Engineering, "bookmark", map[string]any{"url": "https://plakar.io", "caption": []any{}})
	s.AddBlock(d.Engineering, "link_preview", map[string]any{"url": "https://github.com/PlakarKorp/plakar"})
	s.AddBlock(d.Engineering, "unsupported", map[string]any{})
	s.AddBlock(d.Engineering, "divider", map[string]any{})
	d.Synced = s.AddBlock(d.Engineering, "synced_block", map[string]any{"synced_from": nil})
	s.AddBlock(d.Synced, "paragraph", map[string]any{"rich_text": Text("synced content"), "color": "default"})
	d.ChildB = s.AddPage("page_id", d.Engineering, "Child B", nil)
	s.AddBlock(d.ChildB, "synced_block", map[string]any{"synced_from": map[string]any{"type": "block_id", "block_id": d.Synced}})
	s.AddBlock(d.Engineering, "pdf", map[string]any{"type": "file", "file": d.Spec["file"], "caption": []any{}})
	s.AddBlock(d.Engineering, "code", map[string]any{"rich_text": Text("fmt.Println()"), "language": "go", "caption": []any{}})

	d.Projects, d.ProjectsDS = s.AddDatabase("workspace", "", "Projects", map[string]any{
		"Name": map[string]any{"type": "title", "title": map[string]any{}},
	})
	d.ProjectRow = s.AddPage("data_source_id", d.ProjectsDS, "Plakar", nil)
	for i := 1; i <= 30; i++ {
		d.MoreProjects = append(d.MoreProjects, s.AddPage("data_source_id", d.ProjectsDS, fmt.Sprintf("Project %02d", i), nil))
	}
	// a wiki: a page parented by the database itself
	d.Wiki = s.AddPage("database_id", d.Projects, "Guide", nil)
	s.AddBlock(d.Wiki, "paragraph", map[string]any{"rich_text": Text("how to"), "color": "default"})

	d.Tasks, d.TasksDS = s.AddDatabase("page_id", d.Engineering, "Tasks", map[string]any{
		"Task":     map[string]any{"type": "title", "title": map[string]any{}},
		"Status":   map[string]any{"type": "status", "status": map[string]any{"options": []any{option("Todo", "gray"), option("Doing", "blue"), option("Done", "green")}}},
		"Priority": map[string]any{"type": "select", "select": map[string]any{"options": []any{option("High", "red"), option("Low", "gray")}}},
		"Tags":     map[string]any{"type": "multi_select", "multi_select": map[string]any{"options": []any{option("backend", "purple"), option("ui", "pink")}}},
		"Due":      map[string]any{"type": "date", "date": map[string]any{}},
		"Owner":    map[string]any{"type": "people", "people": map[string]any{}},
		"Done":     map[string]any{"type": "checkbox", "checkbox": map[string]any{}},
		"Estimate": map[string]any{"type": "number", "number": map[string]any{"format": "number"}},
		"Files":    map[string]any{"type": "files", "files": map[string]any{}},
		"Project": map[string]any{"type": "relation", "relation": map[string]any{
			"data_source_id": d.ProjectsDS, "database_id": d.Projects, "type": "dual_property",
			"dual_property": map[string]any{"synced_property_name": "Tasks"},
		}},
		"Count":   map[string]any{"type": "rollup", "rollup": map[string]any{"relation_property_name": "Project", "rollup_property_name": "Name", "function": "count"}},
		"Twice":   map[string]any{"type": "formula", "formula": map[string]any{"expression": `prop("Estimate") * 2`}},
		"Created": map[string]any{"type": "created_time", "created_time": map[string]any{}},
		"ID":      map[string]any{"type": "unique_id", "unique_id": map[string]any{"prefix": "TASK"}},
	})
	for i, name := range []string{"Write docs", "Fix bug", "Ship"} {
		props := map[string]any{
			"Status":   map[string]any{"status": map[string]any{"id": "o1", "name": []string{"Todo", "Doing", "Done"}[i], "color": "gray"}},
			"Priority": map[string]any{"select": map[string]any{"id": "o2", "name": "High", "color": "red"}},
			"Tags":     map[string]any{"multi_select": []any{map[string]any{"id": "o3", "name": "backend", "color": "purple"}}},
			"Due":      map[string]any{"date": map[string]any{"start": "2026-12-01", "end": nil, "time_zone": nil}},
			"Owner":    map[string]any{"people": []any{map[string]any{"object": "user", "id": d.Alice}}},
			"Done":     map[string]any{"checkbox": i == 2},
			"Estimate": map[string]any{"number": float64(i + 1)},
			"Project":  map[string]any{"relation": []any{map[string]any{"id": d.ProjectRow}}},
		}
		if i == 2 {
			// more than a page object carries
			rel := []any{}
			for _, id := range d.MoreProjects {
				rel = append(rel, map[string]any{"id": id})
			}
			props["Project"] = map[string]any{"relation": rel}
		}
		if i == 0 {
			att := map[string]any{"name": "notes.txt", "type": "file", "file": d.Attachment["file"]}
			props["Files"] = map[string]any{"files": []any{att, map[string]any{"name": "ext", "type": "external", "external": map[string]any{"url": "https://example.com/ext.txt"}}}}
		}
		id := s.AddPage("data_source_id", d.TasksDS, name, props)
		s.AddBlock(id, "paragraph", map[string]any{"rich_text": Text("about " + name), "color": "default"})
		d.TaskRows = append(d.TaskRows, id)
	}

	_, disc := s.AddComment(d.Engineering, "", d.Alice, "Looks good", []map[string]any{d.Attachment})
	s.AddComment(d.Engineering, disc, d.Bob, "Agreed", nil)
	s.AddComment(d.Paragraph, "", d.Bob, "typo here", nil)

	d.ViewAll = s.AddView(d.Tasks, d.TasksDS, "All tasks", "table", map[string]any{
		"type":       "table",
		"properties": []any{map[string]any{"property_id": s.propertyID(d.TasksDS, "Status"), "visible": true}},
	})
	d.ViewBoard = s.AddView(d.Tasks, d.TasksDS, "Board", "board", map[string]any{
		"type":     "board",
		"group_by": map[string]any{"type": "status", "property_id": s.propertyID(d.TasksDS, "Status"), "sort": "manual"},
	})

	d.Orphan = s.AddPage("page_id", d.Engineering, "Orphan", nil)
	s.detach(d.Orphan)
	s.AddBlock(d.Orphan, "paragraph", map[string]any{"rich_text": Text("lost and found"), "color": "default"})

	d.Trashed = s.AddPage("page_id", d.Engineering, "Trashed", nil)
	s.trash(d.Trashed)
	return d
}

func option(name, color string) map[string]any {
	return map[string]any{"id": "opt-" + name, "name": name, "color": color}
}

// detach makes a page's parent unreachable: the page stays listed by
// search, its parent id answers 404.
func (s *Server) detach(pageID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.objects[pageID]
	parent := s.lookup(o.parentID)
	for i, c := range parent.children {
		if c == pageID {
			parent.children = append(parent.children[:i], parent.children[i+1:]...)
		}
	}
	gone := s.newID()
	o.raw["parent"] = parentRef("page_id", gone)
	o.parentID = gone
	delete(s.objects, "block:"+pageID)
}

func (s *Server) trash(pageID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.objects[pageID]
	o.raw["in_trash"] = true
	o.raw["archived"] = true
	parent := s.lookup(o.parentID)
	for i, c := range parent.children {
		if c == pageID {
			parent.children = append(parent.children[:i], parent.children[i+1:]...)
		}
	}
	delete(s.objects, "block:"+pageID)
}

func (s *Server) propertyID(dsID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.objects[dsID].raw["properties"].(map[string]any)[name].(map[string]any)
	return p["id"].(string)
}

// PropertyID returns the id of a data source property.
func (s *Server) PropertyID(dsID, name string) string { return s.propertyID(dsID, name) }
