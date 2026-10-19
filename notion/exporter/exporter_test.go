package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/importer"
	"github.com/PlakarKorp/integrations/notion/internal/fakenotion"
	"github.com/PlakarKorp/integrations/notion/layout"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/require"
)

// source is a fake workspace with one of everything the replay has to
// handle, and a separate page to restore under.
type source struct {
	srv *fakenotion.Server

	root, child, underToggle, inColumn, rowChild, target string
	db                                                   string
	rows                                                 []string
	toggle, image, columnList, link                      string
	imageBytes, iconBytes                                []byte
}

func text(s string) map[string]any {
	return map[string]any{"rich_text": fakenotion.RichText(s), "color": "default"}
}

func newSource(t *testing.T) *source {
	t.Helper()
	srv := fakenotion.New(t)
	s := &source{srv: srv, imageBytes: []byte("\x89PNG bytes"), iconBytes: []byte("<svg/>")}

	s.root = srv.AddPage("workspace", "", "Root", nil)
	srv.SetField(s.root, "icon", srv.AddFile("icon.svg", s.iconBytes))
	srv.AddBlock(s.root, "paragraph", text("hello"))

	s.toggle = srv.AddBlock(s.root, "toggle", text("more"))
	inner := srv.AddBlock(s.toggle, "paragraph", text("inside"))
	srv.AddBlock(inner, "bulleted_list_item", text("deeper"))
	s.underToggle = srv.AddPage("block_id", s.toggle, "Under a toggle", nil)

	s.columnList = srv.AddBlock(s.root, "column_list", map[string]any{})
	left := srv.AddBlock(s.columnList, "column", map[string]any{})
	right := srv.AddBlock(s.columnList, "column", map[string]any{})
	leftPara := srv.AddBlock(left, "paragraph", text("left"))
	srv.AddBlock(leftPara, "to_do", map[string]any{"rich_text": fakenotion.RichText("third level"), "checked": true, "color": "default"})
	srv.AddBlock(right, "paragraph", text("right"))
	s.inColumn = srv.AddPage("block_id", right, "In a column", nil)

	table := srv.AddBlock(s.root, "table", map[string]any{"table_width": 2, "has_column_header": true, "has_row_header": false})
	srv.AddBlock(table, "table_row", map[string]any{"cells": []any{fakenotion.RichText("a"), fakenotion.RichText("b")}})
	srv.AddBlock(table, "table_row", map[string]any{"cells": []any{fakenotion.RichText("c"), fakenotion.RichText("d")}})

	img := srv.AddFile("pic.png", s.imageBytes)
	img["caption"] = []any{}
	s.image = srv.AddBlock(s.root, "image", img)
	srv.AddBlock(s.root, "image", map[string]any{"type": "external", "external": map[string]any{"url": "https://example.com/x.png"}, "caption": []any{}})
	srv.AddBlock(s.root, "callout", map[string]any{"rich_text": fakenotion.RichText("note"), "icon": map[string]any{"type": "emoji", "emoji": "💡"}, "color": "gray_background"})

	s.child = srv.AddPage("page_id", s.root, "Child", nil)
	srv.AddBlock(s.child, "paragraph", text("child text"))
	s.link = srv.AddBlock(s.root, "link_to_page", map[string]any{"type": "page_id", "page_id": s.child})

	s.db = srv.AddDatabase(s.root, "Tasks", map[string]any{
		"Done": map[string]any{"id": "d", "name": "Done", "type": "checkbox", "checkbox": map[string]any{}},
		"Tag":  map[string]any{"id": "t", "name": "Tag", "type": "select", "select": map[string]any{"options": []any{map[string]any{"id": "o", "name": "urgent", "color": "red"}}}},
		"Roll": map[string]any{"id": "r", "name": "Roll", "type": "rollup", "rollup": map[string]any{}},
	})
	for i, title := range []string{"first", "second"} {
		id := srv.AddPage("database_id", s.db, title, map[string]any{
			"Done": map[string]any{"id": "d", "type": "checkbox", "checkbox": i == 0},
			"Tag":  map[string]any{"id": "t", "type": "select", "select": map[string]any{"id": "o", "name": "urgent", "color": "red"}},
			"Roll": map[string]any{"id": "r", "type": "rollup", "rollup": map[string]any{"type": "number", "number": 1}},
		})
		s.rows = append(s.rows, id)
	}
	s.rowChild = srv.AddPage("page_id", s.rows[0], "Row child", nil)

	s.target = srv.AddPage("workspace", "", "Restore here", nil)
	return s
}

// backup runs the importer on the fake and returns every record, as kloset
// would hand them to an exporter.
func backup(t *testing.T, srv *fakenotion.Server, location string) []*connectors.Record {
	t.Helper()
	imp, err := importer.NewImporter(context.Background(), &connectors.Options{MaxConcurrency: 2}, "notion",
		map[string]string{"location": location, "token": fakenotion.Token, "api_url": srv.URL()})
	require.NoError(t, err)
	records := make(chan *connectors.Record, 4096)
	require.NoError(t, imp.Import(context.Background(), records, nil))
	var out []*connectors.Record
	for r := range records {
		require.NoError(t, r.Err, r.Pathname)
		out = append(out, r)
	}
	return out
}

// rebase strips prefix from the records' paths, which is what plakar does
// when restoring a subtree.
func rebase(records []*connectors.Record, prefix string) []*connectors.Record {
	var out []*connectors.Record
	for _, r := range records {
		if r.Pathname != prefix && !strings.HasPrefix(r.Pathname, prefix+"/") {
			continue
		}
		p := strings.TrimPrefix(r.Pathname, prefix)
		if p == "" {
			p = "/"
		}
		r.Pathname = p
		out = append(out, r)
	}
	return out
}

func newExp(t *testing.T, srv *fakenotion.Server, location string, extra map[string]string) *Exporter {
	t.Helper()
	config := map[string]string{"location": location, "token": fakenotion.Token, "api_url": srv.URL()}
	for k, v := range extra {
		config[k] = v
	}
	exp, err := NewExporter(context.Background(), &connectors.Options{MaxConcurrency: 2}, "notion", config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, exp.Close(context.Background())) })
	return exp.(*Exporter)
}

// restore feeds records to the exporter and returns the result per path.
func restore(t *testing.T, ctx context.Context, exp *Exporter, records []*connectors.Record) (map[string]error, error) {
	t.Helper()
	in := make(chan *connectors.Record)
	results := make(chan *connectors.Result, len(records)+1)
	go func() {
		defer close(in)
		for _, r := range records {
			select {
			case in <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	err := exp.Export(ctx, in, results)
	out := map[string]error{}
	for res := range results {
		key := res.Record.Pathname
		if res.Record.IsXattr {
			key += "@" + res.Record.XattrName
		}
		_, dup := out[key]
		require.False(t, dup, "two results for %s", key)
		out[key] = res.Err
	}
	return out, err
}

// requireAllOk checks every record was answered without error, except
// the paths in lossy, whose error must mention the given text.
func requireAllOk(t *testing.T, results map[string]error, records []*connectors.Record, lossy map[string]string) {
	t.Helper()
	require.Len(t, results, len(records))
	for p, err := range results {
		if want, ok := lossy[p]; ok {
			require.ErrorContains(t, err, want, p)
			continue
		}
		require.NoError(t, err, p)
	}
}

func plainOf(block map[string]any) string {
	typ, _ := block["type"].(string)
	payload, _ := block[typ].(map[string]any)
	rt, _ := payload["rich_text"].([]any)
	return api.PlainText(rt)
}

func titleOf(t *testing.T, page map[string]any) string {
	t.Helper()
	return api.TitleOf(page)
}

func TestExportRoundTrip(t *testing.T) {
	s := newSource(t)
	records := backup(t, s.srv, "notion://"+s.root)
	exp := newExp(t, s.srv, "notion://"+s.target, nil)
	require.Equal(t, fakenotion.WorkspaceName, exp.Origin())
	require.Equal(t, "notion", exp.Type())
	require.Equal(t, s.target, exp.Root())
	require.NoError(t, exp.Ping(context.Background()))

	results, err := restore(t, context.Background(), exp, records)
	require.NoError(t, err)
	// The rollup column of Tasks cannot be created, and says so.
	dbFile := path.Join(layout.DatabaseDir(layout.PageDir("", s.root), s.db), layout.DatabaseFile)
	requireAllOk(t, results, records, map[string]string{dbFile: "Roll"})

	// The target page got one child: a copy of Root.
	under := s.srv.Children(s.target)
	require.Len(t, under, 1)
	require.Equal(t, "child_page", under[0]["type"])
	newRoot := under[0]["id"].(string)
	require.NotEqual(t, s.root, newRoot)
	rootPage := s.srv.Page(newRoot)
	require.Equal(t, "Root", titleOf(t, rootPage))
	icon := rootPage["icon"].(map[string]any)
	require.Equal(t, "file_upload", icon["type"])
	require.Equal(t, s.iconBytes, s.srv.Upload(icon["file_upload"].(map[string]any)["id"].(string)))

	blocks := s.srv.Children(newRoot)
	var types []string
	for _, b := range blocks {
		types = append(types, b["type"].(string))
	}
	// Same order as the source, except the page that lived under the
	// toggle: the API cannot create a page under a block, so it lands in
	// the page itself, right after the batch the toggle came in.
	require.Equal(t, []string{
		"paragraph", "toggle", "column_list", "table", "image", "image", "callout",
		"child_page", "child_page", "child_page", "link_to_page", "child_database",
	}, types)
	require.Equal(t, "hello", plainOf(blocks[0]))

	// Nesting below the toggle survives, three levels down.
	toggleKids := s.srv.Children(blocks[1]["id"].(string))
	require.Len(t, toggleKids, 1)
	require.Equal(t, "inside", plainOf(toggleKids[0]))
	deeper := s.srv.Children(toggleKids[0]["id"].(string))
	require.Len(t, deeper, 1)
	require.Equal(t, "deeper", plainOf(deeper[0]))

	// Columns were created inline, their third level appended after.
	columns := s.srv.Children(blocks[2]["id"].(string))
	require.Len(t, columns, 2)
	leftKids := s.srv.Children(columns[0]["id"].(string))
	require.Len(t, leftKids, 1)
	require.Equal(t, "left", plainOf(leftKids[0]))
	third := s.srv.Children(leftKids[0]["id"].(string))
	require.Len(t, third, 1)
	require.Equal(t, "to_do", third[0]["type"])
	require.Equal(t, true, third[0]["to_do"].(map[string]any)["checked"])
	rightKids := s.srv.Children(columns[1]["id"].(string))
	require.Len(t, rightKids, 1)
	require.Equal(t, "right", plainOf(rightKids[0]))
	require.Len(t, s.srv.Children(blocks[8]["id"].(string)), 0)

	rows := s.srv.Children(blocks[3]["id"].(string))
	require.Len(t, rows, 2)
	require.Equal(t, "table_row", rows[1]["type"])
	cells := rows[1]["table_row"].(map[string]any)["cells"].([]any)
	require.Equal(t, "d", api.PlainText(cells[1].([]any)))

	// The hosted image was uploaded again, the external one kept its link.
	img := blocks[4]["image"].(map[string]any)
	require.Equal(t, "file_upload", img["type"])
	require.Equal(t, s.imageBytes, s.srv.Upload(img["file_upload"].(map[string]any)["id"].(string)))
	require.Equal(t, "external", blocks[5]["image"].(map[string]any)["type"])
	require.Equal(t, "💡", blocks[6]["callout"].(map[string]any)["icon"].(map[string]any)["emoji"])

	require.Equal(t, "Under a toggle", titleOf(t, s.srv.Page(blocks[7]["id"].(string))))
	require.Equal(t, "In a column", titleOf(t, s.srv.Page(blocks[8]["id"].(string))))

	// The child page came out with its content, and the link now points
	// at the copy.
	newChild := blocks[9]["id"].(string)
	require.Equal(t, "Child", titleOf(t, s.srv.Page(newChild)))
	childKids := s.srv.Children(newChild)
	require.Len(t, childKids, 1)
	require.Equal(t, "child text", plainOf(childKids[0]))
	require.Equal(t, newChild, blocks[10]["link_to_page"].(map[string]any)["page_id"])

	// The database kept the schema the API can create and its rows, in
	// order, less the rollup; a page under a row is under the new row.
	newDB := blocks[11]["id"].(string)
	db := s.srv.Database(newDB)
	require.Equal(t, "Tasks", titleOf(t, db))
	schema := db["properties"].(map[string]any)
	require.ElementsMatch(t, []string{"Name", "Done", "Tag"}, keys(schema))
	newRows := s.srv.Rows(newDB)
	require.Len(t, newRows, 2)
	require.Equal(t, "first", titleOf(t, newRows[0]))
	require.Equal(t, "second", titleOf(t, newRows[1]))
	props := newRows[0]["properties"].(map[string]any)
	require.Equal(t, true, props["Done"].(map[string]any)["checkbox"])
	require.Equal(t, "urgent", props["Tag"].(map[string]any)["select"].(map[string]any)["name"])
	require.NotContains(t, props, "Roll")
	rowKids := s.srv.Children(newRows[0]["id"].(string))
	require.Len(t, rowKids, 1)
	require.Equal(t, "Row child", titleOf(t, s.srv.Page(rowKids[0]["id"].(string))))

	// Nothing was written outside the staging directory, which is gone
	// once the exporter is closed.
	tmp := exp.tmpDir
	require.NoError(t, exp.Close(context.Background()))
	_, err = os.Stat(tmp)
	require.True(t, os.IsNotExist(err))
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestExportSubtree(t *testing.T) {
	s := newSource(t)

	t.Run("page", func(t *testing.T) {
		records := rebase(backup(t, s.srv, "notion://"+s.child), layout.PageDir("", s.child))
		var paths []string
		for _, r := range records {
			paths = append(paths, r.Pathname)
		}
		require.ElementsMatch(t, []string{"/", "/page.json", "/blocks.json"}, paths)

		target := s.srv.AddPage("workspace", "", "Target", nil)
		results, err := restore(t, context.Background(), newExp(t, s.srv, "notion://"+target, nil), records)
		require.NoError(t, err)
		requireAllOk(t, results, records, nil)
		under := s.srv.Children(target)
		require.Len(t, under, 1)
		require.Equal(t, "Child", titleOf(t, s.srv.Page(under[0]["id"].(string))))
	})

	t.Run("row becomes a page", func(t *testing.T) {
		all := backup(t, s.srv, "notion://"+s.db)
		rowDir := layout.PageDir(layout.DatabaseDir("", s.db), s.rows[1])
		records := rebase(all, rowDir)
		require.NotEmpty(t, records)

		target := s.srv.AddPage("workspace", "", "Target", nil)
		results, err := restore(t, context.Background(), newExp(t, s.srv, "notion://"+target, nil), records)
		require.NoError(t, err)
		requireAllOk(t, results, records, nil)
		under := s.srv.Children(target)
		require.Len(t, under, 1)
		page := s.srv.Page(under[0]["id"].(string))
		require.Equal(t, "second", titleOf(t, page))
		require.Equal(t, []string{"title"}, keys(page["properties"].(map[string]any)))
	})

	t.Run("database", func(t *testing.T) {
		records := rebase(backup(t, s.srv, "notion://"+s.db), layout.DatabaseDir("", s.db))
		target := s.srv.AddPage("workspace", "", "Target", nil)
		results, err := restore(t, context.Background(), newExp(t, s.srv, "notion://"+target, nil), records)
		require.NoError(t, err)
		requireAllOk(t, results, records, map[string]string{"/" + layout.DatabaseFile: "Roll"})
		under := s.srv.Children(target)
		require.Len(t, under, 1)
		require.Equal(t, "child_database", under[0]["type"])
		require.Len(t, s.srv.Rows(under[0]["id"].(string)), 2)
	})
}

// file builds a regular file record.
func file(pathname string, data []byte) *connectors.Record {
	return connectors.NewRecord(pathname, "", objects.FileInfo{Lname: path.Base(pathname), Lmode: 0o600, Lsize: int64(len(data)), LmodTime: time.Now()}, nil,
		func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(data))), nil })
}

func dir(pathname string) *connectors.Record {
	return connectors.NewRecord(pathname, "", objects.FileInfo{Lname: path.Base(pathname), Lmode: os.ModeDir | 0o700, LmodTime: time.Now()}, nil, nil)
}

func pageJSON(t *testing.T, id, title string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"object": "page", "id": id, "parent": fakenotion.Parent("workspace", ""),
		"created_time": "2026-01-01T00:00:00Z", "last_edited_time": "2026-01-01T00:00:00Z",
		"properties": map[string]any{"title": map[string]any{"id": "title", "type": "title", "title": fakenotion.RichText(title)}},
	})
	require.NoError(t, err)
	return data
}

func blocksJSON(t *testing.T, blocks ...map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(blocks)
	require.NoError(t, err)
	return data
}

func para(id, s string) map[string]any {
	return map[string]any{"object": "block", "id": id, "type": "paragraph", "paragraph": text(s), "has_children": false}
}

func TestExportReportsWhatWasLost(t *testing.T) {
	s := newSource(t)
	const pid = "00000000-0000-0000-0000-00000000aaaa"
	page := layout.PageDir("", pid)
	// A record is read once, so each subtest gets its own.
	records := func() []*connectors.Record {
		return []*connectors.Record{
			dir("/"), dir("/pages"), dir(page),
			file(page+"/page.json", pageJSON(t, pid, "Lossy")),
			file(page+"/blocks.json", blocksJSON(t,
				para("b1", "kept"),
				map[string]any{"object": "block", "id": "b2", "type": "unsupported", "unsupported": map[string]any{}},
				map[string]any{"object": "block", "id": "b3", "type": "image", "image": map[string]any{"type": "file", "file": map[string]any{"url": "https://gone", "expiry_time": "t"}}},
				map[string]any{"object": "block", "id": "b4", "type": "pdf", "pdf": map[string]any{"type": "file", "file": map[string]any{"url": "https://x/doc.pdf", "expiry_time": "t"}}},
				para("b5", "also kept"),
			)),
			dir(page + "/files"),
			file(page+"/files/b4.pdf", []byte("%PDF-")),
			connectors.NewError(page+"/pages", fmt.Errorf("the backup already failed here")),
			connectors.NewXattr(page+"/page.json", "user.x", objects.AttributeExtended, func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }),
		}
	}

	t.Run("blocks that cannot come back", func(t *testing.T) {
		target := s.srv.AddPage("workspace", "", "Target", nil)
		records := records()
		results, err := restore(t, context.Background(), newExp(t, s.srv, "notion://"+target, nil), records)
		require.NoError(t, err)
		require.Len(t, results, len(records))
		for _, p := range []string{"/", "/pages", page, page + "/page.json", page + "/files", page + "/files/b4.pdf", page + "/pages"} {
			require.NoError(t, results[p], p)
		}
		err = results[page+"/blocks.json"]
		require.Error(t, err)
		require.Contains(t, err.Error(), "b2")
		require.Contains(t, err.Error(), "unsupported")
		require.Contains(t, err.Error(), "b3")
		require.Contains(t, err.Error(), "not in the backup")
		require.NotContains(t, err.Error(), "b4")

		under := s.srv.Children(target)
		require.Len(t, under, 1)
		blocks := s.srv.Children(under[0]["id"].(string))
		require.Len(t, blocks, 3)
		require.Equal(t, "kept", plainOf(blocks[0]))
		require.Equal(t, "pdf", blocks[1]["type"])
		require.Equal(t, "also kept", plainOf(blocks[2]))
	})

	t.Run("skipping attachments is reported on them", func(t *testing.T) {
		target := s.srv.AddPage("workspace", "", "Target", nil)
		results, err := restore(t, context.Background(), newExp(t, s.srv, "notion://"+target, map[string]string{"skip_attachments": "true"}), records())
		require.NoError(t, err)
		require.ErrorContains(t, results[page+"/files/b4.pdf"], "skipped")
		require.ErrorContains(t, results[page+"/blocks.json"], "b4")
		blocks := s.srv.Children(s.srv.Children(target)[0]["id"].(string))
		require.Len(t, blocks, 2)
	})

	t.Run("children of a page whose blocks are unreadable are not restored", func(t *testing.T) {
		const cid = "00000000-0000-0000-0000-00000000cccc"
		childDir := layout.PageDir(page, cid)
		records := []*connectors.Record{
			dir("/"), dir("/pages"), dir(page),
			file(page+"/page.json", pageJSON(t, pid, "Parent")),
			file(page+"/blocks.json", []byte("[not json")),
			dir(page + "/pages"), dir(childDir),
			file(childDir+"/page.json", pageJSON(t, cid, "Orphaned")),
			file(childDir+"/blocks.json", blocksJSON(t, para("b1", "x"))),
		}
		target := s.srv.AddPage("workspace", "", "Target", nil)
		results, err := restore(t, context.Background(), newExp(t, s.srv, "notion://"+target, nil), records)
		require.NoError(t, err)
		require.NoError(t, results[page+"/page.json"])
		require.ErrorContains(t, results[page+"/blocks.json"], "decode")
		for _, p := range []string{childDir, childDir + "/page.json", childDir + "/blocks.json"} {
			require.ErrorContains(t, results[p], "was not restored", p)
		}
		under := s.srv.Children(target)
		require.Len(t, under, 1)
		require.Empty(t, s.srv.Children(under[0]["id"].(string)))
	})

	t.Run("a page that fails takes its tree with it", func(t *testing.T) {
		broken := []*connectors.Record{
			dir("/"), dir("/pages"), dir(page),
			file(page+"/page.json", []byte("not json")),
			file(page+"/blocks.json", blocksJSON(t, para("b1", "x"))),
		}
		target := s.srv.AddPage("workspace", "", "Target", nil)
		results, err := restore(t, context.Background(), newExp(t, s.srv, "notion://"+target, nil), broken)
		require.NoError(t, err)
		require.NoError(t, results["/"])
		require.NoError(t, results["/pages"])
		for _, p := range []string{page, page + "/page.json", page + "/blocks.json"} {
			require.ErrorContains(t, results[p], "decode", p)
		}
		require.Empty(t, s.srv.Children(target))
	})
}

func TestExportBatchesAndIsolatesBadBlocks(t *testing.T) {
	s := newSource(t)
	const pid = "00000000-0000-0000-0000-00000000bbbb"
	page := layout.PageDir("", pid)
	var blocks []map[string]any
	for i := 0; i < 250; i++ {
		blocks = append(blocks, para(fmt.Sprintf("b%03d", i), fmt.Sprintf("p%03d", i)))
	}
	blocks[120] = map[string]any{"object": "block", "id": "bad", "type": "bogus", "bogus": map[string]any{}}
	records := []*connectors.Record{
		dir("/"), dir("/pages"), dir(page),
		file(page+"/page.json", pageJSON(t, pid, "Long")),
		file(page+"/blocks.json", blocksJSON(t, blocks...)),
	}
	target := s.srv.AddPage("workspace", "", "Target", nil)
	exp := newExp(t, s.srv, "notion://"+target, nil)
	before := s.srv.Requests()
	results, err := restore(t, context.Background(), exp, records)
	require.NoError(t, err)
	require.ErrorContains(t, results[page+"/blocks.json"], "bad")
	require.ErrorContains(t, results[page+"/blocks.json"], "bogus")

	created := s.srv.Children(s.srv.Children(target)[0]["id"].(string))
	require.Len(t, created, 249)
	require.Equal(t, "p000", plainOf(created[0]))
	require.Equal(t, "p119", plainOf(created[119]))
	require.Equal(t, "p121", plainOf(created[120]))
	require.Equal(t, "p249", plainOf(created[248]))
	// One page, three batches, and the failing batch again one by one.
	require.Equal(t, 1+3+100, s.srv.Requests()-before)
}

func TestExportStopsOnCancel(t *testing.T) {
	s := newSource(t)
	records := backup(t, s.srv, "notion://"+s.root)
	exp := newExp(t, s.srv, "notion://"+s.target, nil)

	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan *connectors.Record)
	results := make(chan *connectors.Result, len(records))
	done := make(chan error, 1)
	go func() { done <- exp.Export(ctx, in, results) }()
	in <- records[0]
	in <- records[1]
	cancel()
	// kloset keeps sending until its own walk notices, then closes.
	in <- records[2]
	close(in)
	require.ErrorIs(t, <-done, context.Canceled)
	var n int
	for res := range results {
		n++
		require.ErrorIs(t, res.Err, context.Canceled)
	}
	require.Equal(t, 3, n)
	require.Empty(t, s.srv.Children(s.target))
}

func TestNewExporterConfig(t *testing.T) {
	srv := fakenotion.New(t)
	target := srv.AddPage("workspace", "", "Target", nil)
	ctx := context.Background()
	opts := &connectors.Options{MaxConcurrency: 1}

	for _, tc := range []struct {
		name   string
		config map[string]string
		want   string
	}{
		{name: "no token", config: map[string]string{"location": "notion://" + target}, want: "missing token"},
		{name: "no target", config: map[string]string{"location": "notion://", "token": fakenotion.Token}, want: "page to restore under"},
		{name: "bad location", config: map[string]string{"location": "notion://nope", "token": fakenotion.Token}, want: "not a notion id"},
		{name: "bad token", config: map[string]string{"location": "notion://" + target, "token": "wrong", "api_url": srv.URL()}, want: "unauthorized"},
		{name: "unknown target", config: map[string]string{"location": "notion://00000000-0000-0000-0000-000000000001", "token": fakenotion.Token, "api_url": srv.URL()}, want: "target page"},
		{name: "bad bool", config: map[string]string{"location": "notion://" + target, "token": fakenotion.Token, "api_url": srv.URL(), "skip_attachments": "maybe"}, want: "skip_attachments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewExporter(ctx, opts, "notion", tc.config)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			if tc.name == "unknown target" {
				require.True(t, api.IsStatus(err, http.StatusNotFound))
			}
		})
	}
}

func TestStagingIsContained(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/a/b", filepath.Join("a", "b")},
		{"/", "."},
		{"", "."},
		{"/../../etc/passwd", filepath.Join("etc", "passwd")},
		{"/a/../../b", "b"},
	} {
		got, err := relative(tc.in)
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.want, got, tc.in)
	}

	s := newSource(t)
	target := s.srv.AddPage("workspace", "", "Target", nil)
	exp := newExp(t, s.srv, "notion://"+target, nil)
	outside := filepath.Join(filepath.Dir(exp.tmpDir), "plakar-notion-escape")
	t.Cleanup(func() { _ = os.Remove(outside) })
	records := []*connectors.Record{
		file("/../plakar-notion-escape", []byte("x")),
		connectors.NewRecord("/link", "/etc/passwd", objects.FileInfo{Lname: "link", Lmode: os.ModeSymlink | 0o777}, nil, nil),
	}
	results, err := restore(t, context.Background(), exp, records)
	require.NoError(t, err)
	_, err = os.Stat(outside)
	require.True(t, os.IsNotExist(err), "a record climbed out of the staging directory")
	require.NoError(t, results["/../plakar-notion-escape"])
	require.ErrorContains(t, results["/link"], "no")
}
