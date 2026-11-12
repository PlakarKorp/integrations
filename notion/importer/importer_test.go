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

package importer

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/internal/fakenotion"
	"github.com/PlakarKorp/integrations/notion/layout"
	"github.com/PlakarKorp/kloset/exclude"
	"github.com/stretchr/testify/require"
)

type backup struct {
	records map[string]*integration.Record
	errors  map[string]error
	paths   []string
}

func (b *backup) read(t *testing.T, p string) []byte {
	t.Helper()
	r, ok := b.records[p]
	require.True(t, ok, "no record for %s", p)
	require.NotNil(t, r.Reader, "no reader for %s", p)
	data, err := io.ReadAll(r.Reader)
	require.NoError(t, err, p)
	require.NoError(t, r.Reader.Close())
	return data
}

func (b *backup) has(p string) bool {
	_, ok := b.records[p]
	return ok
}

func (b *backup) under(prefix string) []string {
	var out []string
	for _, p := range b.paths {
		if strings.HasPrefix(p, prefix) {
			out = append(out, strings.TrimPrefix(p, prefix))
		}
	}
	return out
}

func run(t *testing.T, imp *Importer) *backup {
	t.Helper()
	records := make(chan *integration.Record, 16)
	done := make(chan error, 1)
	go func() { done <- imp.Import(context.Background(), records, nil) }()
	b := &backup{records: map[string]*integration.Record{}, errors: map[string]error{}}
	for r := range records {
		if r.Err != nil {
			b.errors[r.Pathname] = r.Err
			continue
		}
		require.NotContains(t, b.records, r.Pathname, "duplicate record")
		b.records[r.Pathname] = r
		b.paths = append(b.paths, r.Pathname)
	}
	require.NoError(t, <-done)
	sort.Strings(b.paths)
	return b
}

func newImporter(t *testing.T, s *fakenotion.Server, cfg Config, excludes []string) *Importer {
	t.Helper()
	client, err := api.New(api.Config{Token: s.Token, BaseURL: s.URL(), Rate: 10000, Concurrency: 4})
	require.NoError(t, err)
	rules := exclude.NewRuleSet()
	require.NoError(t, rules.AddRulesFromArray(excludes))
	imp, err := New(context.Background(), &integration.Options{MaxConcurrency: 4}, client, cfg, rules)
	require.NoError(t, err)
	return imp
}

func dir(title, id string) string { return layout.DirName(title, id) }

func TestImport(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)

	imp := newImporter(t, s, Config{Comments: CommentsPages, Users: true, Views: true}, nil)
	require.Equal(t, "Fake Workspace", imp.Origin())
	require.Equal(t, "notion", imp.Type())
	require.NoError(t, imp.Ping(context.Background()))

	b := run(t, imp)
	require.Empty(t, b.errors)

	eng := "/pages/" + dir("Engineering", d.Engineering)
	tasks := eng + "/databases/" + dir("Tasks", d.Tasks)
	tasksDS := tasks + "/sources/" + dir("Tasks", d.TasksDS)
	projects := "/databases/" + dir("Projects", d.Projects)

	// the shape of the tree
	for _, p := range []string{
		"/", "/workspace.json", "/users.json", "/pages", "/databases",
		eng, eng + "/about.json", eng + "/blocks.json", eng + "/comments.json", eng + "/content.md",
		eng + "/pages/" + dir("Child A", d.ChildA) + "/about.json",
		eng + "/pages/" + dir("Child B", d.ChildB) + "/blocks.json",
		eng + "/pages/" + dir("Nested", d.Nested) + "/about.json",
		tasks + "/about.json", tasks + "/views.json",
		tasksDS + "/about.json",
		tasksDS + "/rows/" + dir("Write docs", d.TaskRows[0]) + "/about.json",
		tasksDS + "/rows/" + dir("Ship", d.TaskRows[2]) + "/blocks.json",
		projects + "/about.json",
		projects + "/sources/" + dir("Projects", d.ProjectsDS) + "/rows/" + dir("Plakar", d.ProjectRow) + "/about.json",
		"/pages/" + dir("Orphan", d.Orphan) + "/about.json",
	} {
		require.True(t, b.has(p), "missing %s in:\n%s", p, strings.Join(b.paths, "\n"))
	}
	for _, p := range b.paths {
		require.NotContains(t, p, "Trashed")
		require.NotEqual(t, layout.KindUnknown, layout.Classify(p).Kind, "unclassifiable path %s", p)
	}

	// assets: the cover and the embedded files, keyed by file id and name
	assets := b.under(eng + "/assets/")
	require.Len(t, assets, 8, "four files, each in the directory of its file id")
	var names []string
	for _, a := range assets {
		if strings.Contains(a, "/") {
			names = append(names, a[strings.Index(a, "/")+1:])
		}
	}
	sort.Strings(names)
	require.Equal(t, []string{"cover.png", "diagram.png", "notes.txt", "spec.pdf"}, names)
	for _, a := range assets {
		if strings.HasSuffix(a, "diagram.png") {
			require.Equal(t, d.DiagramData, b.read(t, eng+"/assets/"+a))
			require.Equal(t, int64(-1), b.records[eng+"/assets/"+a].FileInfo.Size())
		}
	}
	rowAssets := b.under(tasksDS + "/rows/" + dir("Write docs", d.TaskRows[0]) + "/assets/")
	require.Len(t, rowAssets, 2) // the directory and the file
	require.Equal(t, d.AttachmentData, b.read(t, tasksDS+"/rows/"+dir("Write docs", d.TaskRows[0])+"/assets/"+rowAssets[1]))

	// the block tree is not fetched until read, then complete
	for k, n := range s.Requests {
		require.NotContains(t, k, "children", "block children fetched during the scan: %s x%d", k, n)
	}
	blocks := b.records[eng+"/blocks.json"]
	require.Equal(t, int64(-1), blocks.FileInfo.Size())
	var tree []map[string]any
	require.NoError(t, json.Unmarshal(b.read(t, eng+"/blocks.json"), &tree))
	var types []string
	for _, blk := range tree {
		types = append(types, blk["type"].(string))
	}
	require.Equal(t, []string{"heading_1", "paragraph", "image", "child_page", "toggle", "column_list", "table", "bookmark", "link_preview", "unsupported", "divider", "synced_block", "child_page", "pdf", "code", "child_database"}, types)
	toggle := tree[4]
	kids := toggle["children"].([]any)
	require.Len(t, kids, 2)
	require.Equal(t, "paragraph", kids[0].(map[string]any)["type"])
	require.Equal(t, "child_page", kids[1].(map[string]any)["type"])
	cols := tree[5]["children"].([]any)
	require.Len(t, cols, 2)
	deep := cols[1].(map[string]any)["children"].([]any)[0].(map[string]any)["children"].([]any)
	require.Equal(t, "paragraph", deep[0].(map[string]any)["type"])

	// the markdown keeps the files reachable inside the snapshot
	md := string(b.read(t, eng+"/content.md"))
	require.Contains(t, md, "![the diagram](assets/")
	require.Contains(t, md, "/diagram.png)")
	require.NotContains(t, md, "/image%20", "the fake's names have no spaces")
	require.NotContains(t, md, "exp=")
	require.Contains(t, md, "Child A</page>")

	// comments
	var comments []map[string]any
	require.NoError(t, json.Unmarshal(b.read(t, eng+"/comments.json"), &comments))
	require.Len(t, comments, 2, "page-level comments only")

	// metadata kloset uses for its cache
	about := b.records[eng+"/about.json"]
	require.Positive(t, about.FileInfo.Size())
	require.Equal(t, blocks.FileInfo.ModTime(), about.FileInfo.ModTime())

	// users and workspace
	var users []map[string]any
	require.NoError(t, json.Unmarshal(b.read(t, "/users.json"), &users))
	require.Len(t, users, 3)
	var ws map[string]any
	require.NoError(t, json.Unmarshal(b.read(t, "/workspace.json"), &ws))
	require.Equal(t, api.Version, ws["api_version"])

	// views
	var views []map[string]any
	require.NoError(t, json.Unmarshal(b.read(t, tasks+"/views.json"), &views))
	require.Len(t, views, 2)

	// a relation the page object truncates is read back in full
	var ship map[string]any
	require.NoError(t, json.Unmarshal(b.read(t, tasksDS+"/rows/"+dir("Ship", d.TaskRows[2])+"/about.json"), &ship))
	project := ship["properties"].(map[string]any)["Project"].(map[string]any)
	require.Len(t, project["relation"], 30)
	require.Nil(t, project["has_more"])

	// a page parented by a database lives under it
	require.True(t, b.has(projects+"/pages/"+dir("Guide", d.Wiki)+"/about.json"))
}

func TestImportBlockComments(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)

	b := run(t, newImporter(t, s, Config{Comments: CommentsBlocks}, nil))
	eng := "/pages/" + dir("Engineering", d.Engineering)
	var comments []map[string]any
	require.NoError(t, json.Unmarshal(b.read(t, eng+"/comments.json"), &comments))
	require.Len(t, comments, 3)
	// the tree was fetched eagerly, blocks.json has a size
	require.Positive(t, b.records[eng+"/blocks.json"].FileInfo.Size())
}

func TestImportScoped(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)

	b := run(t, newImporter(t, s, Config{Root: d.ChildA, Comments: CommentsNone, Users: false}, nil))
	require.Equal(t, []string{"/", "/pages",
		"/pages/" + dir("Child A", d.ChildA),
		"/pages/" + dir("Child A", d.ChildA) + "/about.json",
		"/pages/" + dir("Child A", d.ChildA) + "/blocks.json",
		"/pages/" + dir("Child A", d.ChildA) + "/content.md",
		"/workspace.json",
	}, b.paths)

	imp := newImporter(t, s, Config{Root: d.Tasks}, nil)
	b = run(t, imp)
	require.True(t, b.has("/databases/"+dir("Tasks", d.Tasks)+"/sources/"+dir("Tasks", d.TasksDS)+"/rows/"+dir("Ship", d.TaskRows[2])+"/about.json"))
	require.False(t, b.has("/pages/"+dir("Engineering", d.Engineering)))

	_, err := api.New(api.Config{Token: s.Token, BaseURL: s.URL()})
	require.NoError(t, err)
	imp = newImporter(t, s, Config{Root: "00000000-0000-0000-0000-000000000000"}, nil)
	records := make(chan *integration.Record, 100)
	err = imp.Import(context.Background(), records, nil)
	require.Error(t, err)
}

func TestImportExcludes(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)

	b := run(t, newImporter(t, s, Config{}, []string{"/pages/*/databases", "*.md"}))
	eng := "/pages/" + dir("Engineering", d.Engineering)
	require.True(t, b.has(eng+"/blocks.json"))
	require.False(t, b.has(eng+"/content.md"))
	require.False(t, b.has(eng+"/databases/"+dir("Tasks", d.Tasks)+"/about.json"))
	require.True(t, b.has("/databases/"+dir("Projects", d.Projects)+"/about.json"))
}

// TestImportTruncatedMarkdown makes the server cut the markdown short:
// the children hidden in the cut part must still be found.
func TestImportTruncatedMarkdown(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)
	s.MarkdownLimit = 2

	b := run(t, newImporter(t, s, Config{}, nil))
	eng := "/pages/" + dir("Engineering", d.Engineering)
	require.True(t, b.has(eng+"/pages/"+dir("Child B", d.ChildB)+"/about.json"))
	require.True(t, b.has(eng+"/databases/"+dir("Tasks", d.Tasks)+"/about.json"))
	md := string(b.read(t, eng+"/content.md"))
	require.NotContains(t, md, "<unknown")
	require.Contains(t, md, "Child B</page>")
	require.Contains(t, md, "Tasks</database>")
	require.Contains(t, md, "![the diagram](assets/")
	require.NotContains(t, md, "inside child a", "a child page's content must not be inlined")
}

// TestImportExpiredURL reads an asset long after the scan: the signed
// url found then is dead and a fresh one must be fetched.
func TestImportExpiredURL(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)
	now := time.Now()
	s.SetNow(func() time.Time { return now })

	b := run(t, newImporter(t, s, Config{}, nil))
	now = now.Add(3 * time.Hour)

	eng := "/pages/" + dir("Engineering", d.Engineering)
	for _, a := range b.under(eng + "/assets/") {
		if strings.HasSuffix(a, "cover.png") {
			require.Equal(t, d.CoverData, b.read(t, eng+"/assets/"+a))
		}
		if strings.HasSuffix(a, "diagram.png") {
			require.Equal(t, d.DiagramData, b.read(t, eng+"/assets/"+a))
		}
		if strings.HasSuffix(a, "notes.txt") {
			require.Equal(t, d.AttachmentData, b.read(t, eng+"/assets/"+a))
		}
	}
}

func TestImportRateLimited(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)
	s.FailEvery = 7

	b := run(t, newImporter(t, s, Config{}, nil))
	require.Empty(t, b.errors)
	require.True(t, b.has("/pages/"+dir("Engineering", d.Engineering)+"/pages/"+dir("Child B", d.ChildB)+"/about.json"))
}

func TestImportQueryLimit(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)
	s.QueryLimit = 2

	b := run(t, newImporter(t, s, Config{}, nil))
	tasksDS := "/pages/" + dir("Engineering", d.Engineering) + "/databases/" + dir("Tasks", d.Tasks) + "/sources/" + dir("Tasks", d.TasksDS)
	for i, name := range []string{"Write docs", "Fix bug", "Ship"} {
		require.True(t, b.has(tasksDS+"/rows/"+dir(name, d.TaskRows[i])+"/about.json"), name)
	}
}

func TestParseLocation(t *testing.T) {
	id, err := ParseLocation("notion", "notion://")
	require.NoError(t, err)
	require.Empty(t, id)
	id, err = ParseLocation("notion", "notion://11111111222233334444555555555555")
	require.NoError(t, err)
	require.Equal(t, "11111111-2222-3333-4444-555555555555", id)
	id, err = ParseLocation("notion", "notion:///My-Page-11111111222233334444555555555555")
	require.NoError(t, err)
	require.Equal(t, "11111111-2222-3333-4444-555555555555", id)
	_, err = ParseLocation("notion", "notion://nope")
	require.Error(t, err)
}

func TestMtime(t *testing.T) {
	p := &Importer{started: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	old := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, old, p.mtime(old))
	recent := p.started.Add(-30 * time.Second)
	require.Equal(t, p.started, p.mtime(recent))
	require.Equal(t, p.started, p.mtime(time.Time{}))
}

func TestSpliceUnknown(t *testing.T) {
	text := "a\n\n<unknown url=\"https://www.notion.so/p#11111111222233334444555555555555\" />\n\nb"
	out := spliceUnknown(text, "11111111-2222-3333-4444-555555555555", "SUB\n")
	require.Equal(t, "a\n\nSUB\n\nb", out)
	out = spliceUnknown("plain", "11111111-2222-3333-4444-555555555555", "SUB")
	require.Equal(t, "plain\n\nSUB\n", out)
}

// TestImportDataSourceRoot: a data source as the root backs its
// database up, the only place a data source can live.
func TestImportDataSourceRoot(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)

	b := run(t, newImporter(t, s, Config{Root: d.TasksDS, Comments: CommentsNone}, nil))
	require.Empty(t, b.errors)
	require.True(t, b.has("/databases/"+dir("Tasks", d.Tasks)+"/sources/"+dir("Tasks", d.TasksDS)+"/rows/"+dir("Ship", d.TaskRows[2])+"/about.json"))
}

// TestImportHiddenDatabase: a data source whose database the token
// cannot see is reported, not silently dropped.
func TestImportHiddenDatabase(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()
	d := fakenotion.Seed(s)
	s.Hide(d.Projects)

	b := run(t, newImporter(t, s, Config{Comments: CommentsNone}, nil))
	require.True(t, b.has("/pages/"+dir("Engineering", d.Engineering)+"/about.json"))
	require.False(t, b.has("/databases/"+dir("Projects", d.Projects)+"/about.json"))
	err, ok := b.errors["/databases/"+strings.ReplaceAll(d.Projects, "-", "")]
	require.True(t, ok, "errors: %v", b.errors)
	require.ErrorContains(t, err, "not shared with the integration")
}

// TestImportNothingShared: a token that sees nothing yields an error,
// not an empty backup that looks fine.
func TestImportNothingShared(t *testing.T) {
	s := fakenotion.New()
	defer s.Close()

	b := run(t, newImporter(t, s, Config{Comments: CommentsNone}, nil))
	require.ErrorContains(t, b.errors["/"], "nothing to back up")
}
