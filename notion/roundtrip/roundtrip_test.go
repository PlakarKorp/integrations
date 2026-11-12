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

// Package roundtrip backs a fake workspace up with the importer and
// restores it into another with the exporter, the way plakar would in
// between.  Notion offers no sandbox, so a fake stands in for it.
package roundtrip

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/exporter"
	"github.com/PlakarKorp/integrations/notion/importer"
	"github.com/PlakarKorp/integrations/notion/internal/fakenotion"
	"github.com/stretchr/testify/require"
)

// snapshot is what kloset would keep: every record with its content.
type snapshot struct {
	records []*integration.Record
	content map[string][]byte
}

func backup(t *testing.T, s *fakenotion.Server, cfg importer.Config) *snapshot {
	t.Helper()
	client, err := api.New(api.Config{Token: s.Token, BaseURL: s.URL(), Rate: 10000, Concurrency: 4})
	require.NoError(t, err)
	imp, err := importer.New(context.Background(), &integration.Options{MaxConcurrency: 4}, client, cfg, nil)
	require.NoError(t, err)

	records := make(chan *integration.Record, 16)
	done := make(chan error, 1)
	go func() { done <- imp.Import(context.Background(), records, nil) }()
	snap := &snapshot{content: map[string][]byte{}}
	for r := range records {
		require.NoError(t, r.Err, r.Pathname)
		if r.FileInfo.Mode().IsRegular() {
			data, err := io.ReadAll(r.Reader)
			require.NoError(t, err, r.Pathname)
			require.NoError(t, r.Reader.Close())
			snap.content[r.Pathname] = data
			r.FileInfo.Lsize = int64(len(data))
		}
		snap.records = append(snap.records, r)
	}
	require.NoError(t, <-done)
	require.NoError(t, imp.Close(context.Background()))

	// the order kloset walks a snapshot in: depth first, siblings sorted
	sort.SliceStable(snap.records, func(i, j int) bool {
		a := strings.Split(snap.records[i].Pathname, "/")
		b := strings.Split(snap.records[j].Pathname, "/")
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	return snap
}

func restore(t *testing.T, snap *snapshot, s *fakenotion.Server, cfg exporter.Config, filter func(string) bool) map[string]*integration.Result {
	t.Helper()
	client, err := api.New(api.Config{Token: s.Token, BaseURL: s.URL(), Rate: 10000, Concurrency: 2})
	require.NoError(t, err)
	var warnings bytes.Buffer
	exp := exporter.New(&integration.Options{MaxConcurrency: 2, Stderr: &warnings}, client, cfg)

	in := make(chan *integration.Record, len(snap.records))
	for _, r := range snap.records {
		if filter != nil && !filter(r.Pathname) {
			continue
		}
		rr := &integration.Record{Pathname: r.Pathname, FileInfo: r.FileInfo}
		if data, ok := snap.content[r.Pathname]; ok {
			rr.Reader = io.NopCloser(bytes.NewReader(data))
		}
		in <- rr
	}
	close(in)
	out := make(chan *integration.Result, len(snap.records))
	require.NoError(t, exp.Export(context.Background(), in, out))
	results := map[string]*integration.Result{}
	for r := range out {
		results[r.Record.Pathname] = r
	}
	t.Logf("restore warnings:\n%s", warnings.String())
	return results
}

func text(blk map[string]any) string {
	typ := blk["type"].(string)
	payload, _ := blk[typ].(map[string]any)
	var b strings.Builder
	for _, it := range anyList(payload["rich_text"]) {
		b.WriteString(it.(map[string]any)["plain_text"].(string))
	}
	return b.String()
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

func prop(page map[string]any, name string) map[string]any {
	return page["properties"].(map[string]any)[name].(map[string]any)
}

func TestRoundTrip(t *testing.T) {
	src := fakenotion.New()
	defer src.Close()
	d := fakenotion.Seed(src)

	snap := backup(t, src, importer.Config{Comments: importer.CommentsBlocks, Users: true, Views: true})

	dst := fakenotion.New()
	defer dst.Close()
	// same creation order, same ids: the people of the source exist here
	alice := dst.AddUser("Alice", "alice@example.com")
	require.Equal(t, d.Alice, alice)
	dst.AddUser("Bob", "bob@example.com")
	target := dst.AddPage("workspace", "", "Restore", nil)

	results := restore(t, snap, dst, exporter.Config{Parent: target, Comments: true, Views: true}, nil)
	for p, r := range results {
		require.NoError(t, r.Err, p)
	}

	// the top: the page, the orphan and the database the token saw
	top := dst.Titles(target)
	sort.Strings(top)
	require.Equal(t, []string{"Engineering", "Orphan", "Projects"}, top)

	var eng, projects, orphan string
	for _, c := range dst.Children(target) {
		switch dst.Get(c)["object"] {
		case "page":
			if dst.Titles(c) != nil && len(dst.Children(c)) > 3 {
				eng = c
			} else {
				orphan = c
			}
		case "database":
			projects = c
		}
	}
	require.NotEmpty(t, eng)
	require.NotEmpty(t, projects)
	require.Equal(t, []string{"paragraph"}, dst.Titles(orphan))

	// the page itself
	page := dst.Get(eng)
	require.Equal(t, "emoji", page["icon"].(map[string]any)["type"])
	data, name := dst.FileData(page["cover"].(map[string]any))
	require.Equal(t, "cover.png", name)
	require.Equal(t, d.CoverData, data)

	// its blocks, in order, with the child pages back in place; the
	// link preview became a bookmark, the unsupported block is gone,
	// the database and the page that lived in a toggle land at the end
	got := dst.Titles(eng)
	require.Equal(t, []string{
		"heading_1", "paragraph", "image", "Child A", "toggle", "column_list", "table",
		"bookmark", "bookmark", "divider", "synced_block", "Child B", "pdf", "code",
		"Tasks", "Nested",
	}, got)

	kids := dst.Children(eng)
	require.Equal(t, "Welcome", text(dst.Get(kids[0])))
	require.Equal(t, "Hello world", text(dst.Get(kids[1])))

	img := dst.Get(kids[2])
	data, name = dst.FileData(img["image"].(map[string]any))
	require.Equal(t, "diagram.png", name)
	require.Equal(t, d.DiagramData, data)
	require.Equal(t, "the diagram", text(map[string]any{"type": "x", "x": map[string]any{"rich_text": img["image"].(map[string]any)["caption"]}}))

	// what Child A says about Child B was written before Child B
	// existed and patched at the end
	childA := kids[3]
	require.Equal(t, []string{"paragraph", "paragraph", "link_to_page", "paragraph", "paragraph"}, dst.Titles(childA))
	ak := dst.Children(childA)
	childBNew := kids[11]
	mention := dst.Get(ak[1])["paragraph"].(map[string]any)["rich_text"].([]any)[1].(map[string]any)
	require.Equal(t, "mention", mention["type"])
	require.Equal(t, childBNew, mention["mention"].(map[string]any)["page"].(map[string]any)["id"])
	require.Equal(t, childBNew, dst.Get(ak[2])["link_to_page"].(map[string]any)["page_id"])
	link := dst.Get(ak[3])["paragraph"].(map[string]any)["rich_text"].([]any)[0].(map[string]any)["text"].(map[string]any)["link"].(map[string]any)
	require.Equal(t, "https://www.notion.so/"+strings.ReplaceAll(childBNew, "-", ""), link["url"])
	long := dst.Get(ak[4])["paragraph"].(map[string]any)["rich_text"].([]any)
	require.Len(t, long, 2, "a run longer than 2000 characters is split, not cut")
	require.Len(t, text(dst.Get(ak[4])), 2500)

	toggle := kids[4]
	tk := dst.Children(toggle)
	require.Len(t, tk, 1, "the nested page is restored at the end of the page, not in the toggle")
	require.Equal(t, "inside toggle", text(dst.Get(tk[0])))

	cols := dst.Children(kids[5])
	require.Len(t, cols, 2)
	left := dst.Children(cols[0])
	require.Equal(t, []string{"paragraph", "table"}, dst.Titles(cols[0]), "a table in a column, with no placeholder left behind")
	require.Equal(t, "left", text(dst.Get(left[0])))
	inCol := dst.Children(left[1])
	require.Len(t, inCol, 1)
	require.Equal(t, "in a column", dst.Get(inCol[0])["table_row"].(map[string]any)["cells"].([]any)[0].([]any)[0].(map[string]any)["plain_text"])
	right := dst.Children(cols[1])
	require.Equal(t, "right", text(dst.Get(right[0])))
	deep := dst.Children(right[0])
	require.Len(t, deep, 1)
	require.Equal(t, "deep in a column", text(dst.Get(deep[0])))

	rows := dst.Children(kids[6])
	require.Len(t, rows, 2)
	cells := dst.Get(rows[1])["table_row"].(map[string]any)["cells"].([]any)
	require.Equal(t, "d", cells[1].([]any)[0].(map[string]any)["plain_text"])

	require.Equal(t, "https://github.com/PlakarKorp/plakar", dst.Get(kids[8])["bookmark"].(map[string]any)["url"])

	synced := kids[10]
	require.Nil(t, dst.Get(synced)["synced_block"].(map[string]any)["synced_from"])
	require.Equal(t, "synced content", text(dst.Get(dst.Children(synced)[0])))

	childB := kids[11]
	dup := dst.Get(dst.Children(childB)[0])
	require.Equal(t, synced, dup["synced_block"].(map[string]any)["synced_from"].(map[string]any)["block_id"], "the duplicate follows the restored original")

	pdf := dst.Get(kids[12])
	data, _ = dst.FileData(pdf["pdf"].(map[string]any))
	require.Equal(t, d.SpecData, data)
	require.Equal(t, "go", dst.Get(kids[13])["code"].(map[string]any)["language"])

	// the database: schema, rows, relation both ways
	tasks := kids[14]
	tasksDS := dst.Children(tasks)[0]
	schema := dst.Get(tasksDS)["properties"].(map[string]any)
	types := map[string]string{}
	for n, p := range schema {
		types[n] = p.(map[string]any)["type"].(string)
	}
	require.Equal(t, map[string]string{
		"Task": "title", "Status": "status", "Priority": "select", "Tags": "multi_select", "Due": "date",
		"Owner": "people", "Done": "checkbox", "Estimate": "number", "Files": "files", "Project": "relation",
		"Count": "rollup", "Twice": "formula", "Created": "created_time", "ID": "unique_id",
	}, types)
	require.Equal(t, `prop("Estimate") * 2`, schema["Twice"].(map[string]any)["formula"].(map[string]any)["expression"])
	require.Equal(t, "TASK", schema["ID"].(map[string]any)["unique_id"].(map[string]any)["prefix"])
	opts := schema["Status"].(map[string]any)["status"].(map[string]any)["options"].([]any)
	require.Len(t, opts, 3)

	// the wiki page came before the data source: the database was
	// created with a bare data source, which then took the schema
	var projectsDS, wiki string
	for _, c := range dst.Children(projects) {
		switch dst.Get(c)["object"] {
		case "data_source":
			require.Empty(t, projectsDS, "one data source only")
			projectsDS = c
		case "page":
			wiki = c
		}
	}
	require.NotEmpty(t, projectsDS)
	require.NotEmpty(t, wiki)
	require.Equal(t, []string{"paragraph"}, dst.Titles(wiki))
	require.Equal(t, "Projects", text(map[string]any{"type": "x", "x": map[string]any{"rich_text": dst.Get(projectsDS)["title"]}}))
	pschema := dst.Get(projectsDS)["properties"].(map[string]any)
	require.Contains(t, pschema, "Tasks")
	require.Equal(t, "relation", pschema["Tasks"].(map[string]any)["type"])
	require.Equal(t, tasksDS, pschema["Tasks"].(map[string]any)["relation"].(map[string]any)["data_source_id"])
	require.Equal(t, projectsDS, schema["Project"].(map[string]any)["relation"].(map[string]any)["data_source_id"])
	require.Len(t, pschema, 2, "the synced side of the relation is created once")

	taskRows := dst.Children(tasksDS)
	require.Len(t, taskRows, 3)
	var byTitle = map[string]map[string]any{}
	for _, id := range taskRows {
		p := dst.Get(id)
		byTitle[prop(p, "Task")["title"].([]any)[0].(map[string]any)["plain_text"].(string)] = p
	}
	first := byTitle["Write docs"]
	require.NotNil(t, first)
	require.Equal(t, "Todo", prop(first, "Status")["status"].(map[string]any)["name"])
	require.Equal(t, "High", prop(first, "Priority")["select"].(map[string]any)["name"])
	require.Equal(t, "backend", prop(first, "Tags")["multi_select"].([]any)[0].(map[string]any)["name"])
	require.Equal(t, "2026-12-01", prop(first, "Due")["date"].(map[string]any)["start"])
	require.Equal(t, alice, prop(first, "Owner")["people"].([]any)[0].(map[string]any)["id"])
	require.InDelta(t, 1.0, prop(first, "Estimate")["number"], 0)
	files := prop(first, "Files")["files"].([]any)
	require.Len(t, files, 2)
	for _, f := range files {
		fm := f.(map[string]any)
		if fm["type"] == "file" {
			data, name := dst.FileData(fm)
			require.Equal(t, "notes.txt", name)
			require.Equal(t, d.AttachmentData, data)
		} else {
			require.Equal(t, "https://example.com/ext.txt", fm["external"].(map[string]any)["url"])
		}
	}
	projectRows := dst.Children(projectsDS)
	require.Len(t, projectRows, 31)
	rel := prop(first, "Project")["relation"].([]any)
	require.Len(t, rel, 1)
	require.Equal(t, projectRows[0], rel[0].(map[string]any)["id"])
	require.Equal(t, true, prop(byTitle["Ship"], "Done")["checkbox"])
	// a relation longer than a page object carries was backed up in
	// full and restored in full
	var shipRel []any
	for _, id := range dst.Children(dst.Children(tasks)[0]) {
		p := dst.Get(id)
		if prop(p, "Task")["title"].([]any)[0].(map[string]any)["plain_text"] == "Ship" {
			shipRel = dst.FullRelation(id, "Project")
		}
	}
	require.Len(t, shipRel, 30)
	require.Equal(t, []string{"paragraph"}, dst.Titles(first["id"].(string)))

	// comments: the discussion, its reply, the attachment, the author
	comments := dst.Comments(eng)
	require.Len(t, comments, 2)
	require.Equal(t, "Alice", comments[0]["display_name"].(map[string]any)["resolved_name"])
	require.Equal(t, "Bob", comments[1]["display_name"].(map[string]any)["resolved_name"])
	require.Equal(t, comments[0]["discussion_id"], comments[1]["discussion_id"])
	att := comments[0]["attachments"].([]any)
	require.Len(t, att, 1)
	data, _ = dst.FileData(att[0].(map[string]any))
	require.Equal(t, d.AttachmentData, data)
	blockComments := dst.Comments(kids[1])
	require.Len(t, blockComments, 1)
	require.Equal(t, "typo here", text(map[string]any{"type": "x", "x": blockComments[0]}))

	// views, with the property ids rewritten
	views := dst.Views(tasks)
	require.Len(t, views, 2)
	statusID := dst.PropertyID(tasksDS, "Status")
	for _, v := range views {
		cfg := v["configuration"].(map[string]any)
		switch v["name"] {
		case "All tasks":
			require.Equal(t, statusID, cfg["properties"].([]any)[0].(map[string]any)["property_id"])
		case "Board":
			require.Equal(t, statusID, cfg["group_by"].(map[string]any)["property_id"])
		default:
			t.Fatalf("unexpected view %v", v["name"])
		}
	}
}

// TestPartialRestore feeds only a subtree, as plakar restore with a
// path does: the subtree is created under the target, nothing else.
func TestPartialRestore(t *testing.T) {
	src := fakenotion.New()
	defer src.Close()
	d := fakenotion.Seed(src)
	snap := backup(t, src, importer.Config{})

	dst := fakenotion.New()
	defer dst.Close()
	target := dst.AddPage("workspace", "", "Restore", nil)

	prefix := "/pages/Engineering " + strings.ReplaceAll(d.Engineering, "-", "") + "/pages/Child A "
	results := restore(t, snap, dst, exporter.Config{Parent: target}, func(p string) bool {
		return strings.HasPrefix(p, prefix)
	})
	require.NotEmpty(t, results)
	for p, r := range results {
		require.NoError(t, r.Err, p)
	}
	require.Equal(t, []string{"Child A"}, dst.Titles(target))
	childA := dst.Children(target)[0]
	require.Equal(t, "inside child a", text(dst.Get(dst.Children(childA)[0])))
}

func TestRestoreRefusesStrangePaths(t *testing.T) {
	dst := fakenotion.New()
	defer dst.Close()
	target := dst.AddPage("workspace", "", "Restore", nil)
	snap := &snapshot{content: map[string][]byte{"/etc/passwd": []byte("x")}}
	snap.records = []*integration.Record{{Pathname: "/etc/passwd", FileInfo: integration.NewFileInfo("passwd", 1, 0o644, time.Now(), 0, 0, 0, 0, 1)}}
	results := restore(t, snap, dst, exporter.Config{Parent: target}, nil)
	require.Error(t, results["/etc/passwd"].Err)
	require.Empty(t, dst.Children(target))
}
