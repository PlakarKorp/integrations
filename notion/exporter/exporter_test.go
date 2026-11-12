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

package exporter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/internal/fakenotion"
	"github.com/PlakarKorp/integrations/notion/layout"
	"github.com/stretchr/testify/require"
)

const (
	idPage  = "11111111-2222-3333-4444-555555555555"
	idChild = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	idPara1 = "11111111-1111-1111-1111-111111111111"
	idPara2 = "22222222-2222-2222-2222-222222222222"
	idDB    = "33333333-3333-3333-3333-333333333333"
	idDS    = "44444444-4444-4444-4444-444444444444"
	idRow   = "55555555-5555-5555-5555-555555555555"
)

func dirRecord(p string) *integration.Record {
	return &integration.Record{Pathname: p, FileInfo: integration.NewFileInfo(p, 0, os.ModeDir|0o755, time.Now(), 0, 0, 0, 0, 1)}
}

func fileRecord(p string, v any) *integration.Record {
	var data []byte
	switch t := v.(type) {
	case []byte:
		data = t
	case string:
		data = []byte(t)
	default:
		data, _ = json.Marshal(v)
	}
	return &integration.Record{
		Pathname: p,
		FileInfo: integration.NewFileInfo(p, int64(len(data)), 0o644, time.Now(), 0, 0, 0, 0, 1),
		Reader:   io.NopCloser(bytes.NewReader(data)),
	}
}

func pageObject(id, title, parentKind, parentID string) map[string]any {
	parent := map[string]any{"type": parentKind, parentKind: parentID}
	if parentKind == "workspace" {
		parent = map[string]any{"type": "workspace", "workspace": true}
	}
	return map[string]any{
		"object": "page", "id": id,
		"last_edited_time": "2026-01-01T00:00:00.000Z",
		"parent":           parent,
		"properties": map[string]any{
			"title": map[string]any{"id": "title", "type": "title", "title": fakenotion.Text(title)},
		},
	}
}

func block(id, typ string, payload map[string]any, children ...map[string]any) map[string]any {
	b := map[string]any{"object": "block", "id": id, "type": typ, typ: payload, "has_children": len(children) > 0}
	if len(children) > 0 {
		b["children"] = children
	}
	return b
}

func run(t *testing.T, s *fakenotion.Server, cfg Config, records ...*integration.Record) (map[string]*integration.Result, string, error) {
	t.Helper()
	client, err := api.New(api.Config{Token: s.Token, BaseURL: s.URL(), Rate: 10000})
	require.NoError(t, err)
	var warnings bytes.Buffer
	exp := New(&integration.Options{Stderr: &warnings}, client, cfg)
	require.Equal(t, "notion", exp.Type())

	in := make(chan *integration.Record, len(records))
	for _, r := range records {
		in <- r
	}
	close(in)
	out := make(chan *integration.Result, len(records))
	err = exp.Export(context.Background(), in, out)
	results := map[string]*integration.Result{}
	for r := range out {
		results[r.Record.Pathname] = r
	}
	return results, warnings.String(), err
}

// TestPositions checks that a child page goes back between the blocks
// that surrounded it, although its records come after the blocks.
func TestPositions(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	target := s.AddPage("workspace", "", "Target", nil)

	page := "/pages/" + layout.DirName("Root", idPage)
	child := page + "/pages/" + layout.DirName("Kid", idChild)
	results, warnings, err := run(t, s, Config{Parent: target},
		dirRecord("/"), dirRecord("/pages"), dirRecord(page),
		fileRecord(page+"/about.json", pageObject(idPage, "Root", "workspace", "")),
		fileRecord(page+"/blocks.json", []map[string]any{
			block(idPara1, "paragraph", map[string]any{"rich_text": fakenotion.Text("first")}),
			block(idChild, "child_page", map[string]any{"title": "Kid"}),
			block(idPara2, "paragraph", map[string]any{"rich_text": fakenotion.Text("last")}),
		}),
		dirRecord(page+"/pages"), dirRecord(child),
		fileRecord(child+"/about.json", pageObject(idChild, "Kid", "page_id", idPage)),
	)
	require.NoError(t, err)
	require.Empty(t, warnings)
	for p, r := range results {
		require.NoError(t, r.Err, p)
	}
	root := s.Children(target)[0]
	require.Equal(t, []string{"paragraph", "Kid", "paragraph"}, s.Titles(root))
}

func TestStrangePathsAndMissingContent(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	target := s.AddPage("workspace", "", "Target", nil)

	page := "/pages/" + layout.DirName("Root", idPage)
	results, _, err := run(t, s, Config{Parent: target},
		fileRecord("/pages/../../etc/passwd", "x"),
		fileRecord(page+"/about.json", "not json"),
		fileRecord(page+"/blocks.json", "[]"),
		fileRecord("/pages/"+layout.DirName("Other", idChild)+"/blocks.json", "[]"),
	)
	require.NoError(t, err, "per-record errors do not fail the restore")
	require.Error(t, results["/pages/../../etc/passwd"].Err)
	require.Error(t, results[page+"/about.json"].Err)
	require.Error(t, results[page+"/blocks.json"].Err, "blocks of a page that failed")
	require.Error(t, results["/pages/"+layout.DirName("Other", idChild)+"/blocks.json"].Err, "blocks before the page object")
	require.Empty(t, s.Children(target))
}

// TestDroppedBlocks: a media block whose file is not in the backup is
// dropped with a warning, the rest of the page is restored.
func TestDroppedBlocks(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	target := s.AddPage("workspace", "", "Target", nil)

	page := "/pages/" + layout.DirName("Root", idPage)
	_, warnings, err := run(t, s, Config{Parent: target},
		fileRecord(page+"/about.json", pageObject(idPage, "Root", "workspace", "")),
		fileRecord(page+"/blocks.json", []map[string]any{
			block(idPara1, "image", map[string]any{"type": "file", "file": map[string]any{"url": "https://prod-files-secure.s3.us-west-2.amazonaws.com/ws/" + idChild + "/gone.png?x=1", "expiry_time": "x"}}),
			block(idPara2, "paragraph", map[string]any{"rich_text": fakenotion.Text("kept")}),
		}),
	)
	require.Error(t, err, "a lost file fails the restore")
	require.Contains(t, warnings, "file not in the backup")
	root := s.Children(target)[0]
	require.Equal(t, []string{"paragraph"}, s.Titles(root))
}

// TestRowsWithoutUsers: people the target workspace does not know are
// left out rather than failing the row.
func TestRowsWithoutUsers(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	target := s.AddPage("workspace", "", "Target", nil)

	db := "/databases/" + layout.DirName("Tasks", idDB)
	ds := db + "/sources/" + layout.DirName("Tasks", idDS)
	row := ds + "/rows/" + layout.DirName("Task", idRow)
	results, _, err := run(t, s, Config{Parent: target},
		fileRecord(db+"/about.json", map[string]any{
			"object": "database", "id": idDB, "title": fakenotion.Text("Tasks"),
			"parent":       map[string]any{"type": "workspace", "workspace": true},
			"data_sources": []map[string]any{{"id": idDS, "name": "Tasks"}},
		}),
		fileRecord(ds+"/about.json", map[string]any{
			"object": "data_source", "id": idDS, "title": fakenotion.Text("Tasks"),
			"parent": map[string]any{"type": "database_id", "database_id": idDB},
			"properties": map[string]any{
				"Name":  map[string]any{"id": "title", "name": "Name", "type": "title", "title": map[string]any{}},
				"Owner": map[string]any{"id": "o", "name": "Owner", "type": "people", "people": map[string]any{}},
				"Done":  map[string]any{"id": "d", "name": "Done", "type": "checkbox", "checkbox": map[string]any{}},
			},
		}),
		fileRecord(row+"/about.json", map[string]any{
			"object": "page", "id": idRow,
			"parent": map[string]any{"type": "data_source_id", "data_source_id": idDS},
			"properties": map[string]any{
				"Name":  map[string]any{"id": "title", "type": "title", "title": fakenotion.Text("Task")},
				"Owner": map[string]any{"id": "o", "type": "people", "people": []map[string]any{{"object": "user", "id": idChild}}},
				"Done":  map[string]any{"id": "d", "type": "checkbox", "checkbox": true},
			},
		}),
	)
	require.NoError(t, err)
	for p, r := range results {
		require.NoError(t, r.Err, p)
	}
	dbNew := s.Children(target)[0]
	dsNew := s.Children(dbNew)[0]
	rows := s.Children(dsNew)
	require.Len(t, rows, 1)
	props := s.Get(rows[0])["properties"].(map[string]any)
	require.Equal(t, true, props["Done"].(map[string]any)["checkbox"])
	require.Empty(t, props["Owner"].(map[string]any)["people"])
}

func TestParseLocation(t *testing.T) {
	_, err := ParseLocation("notion", "notion://")
	require.Error(t, err, "a restore needs a target page")
	id, err := ParseLocation("notion", "notion://My-Page-11111111222233334444555555555555")
	require.NoError(t, err)
	require.Equal(t, idPage, id)
}
