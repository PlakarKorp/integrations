package importer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/internal/fakenotion"
	"github.com/PlakarKorp/integrations/notion/layout"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/require"
)

// workspace is a fake workspace exercising every kind of edge the walk has
// to take.
type workspace struct {
	srv *fakenotion.Server

	root, child, blockChild, rowChild, orphan string
	db, rootDB                                string
	rows, rootRows                            []string
	toggle, image                             string
	imageBytes                                []byte
}

func newWorkspace(t *testing.T) *workspace {
	t.Helper()
	srv := fakenotion.New(t)
	w := &workspace{srv: srv, imageBytes: []byte("\x89PNG fake image bytes")}

	w.root = srv.AddPage("workspace", "", "Root", nil)
	srv.AddBlock(w.root, "paragraph", map[string]any{"rich_text": fakenotion.RichText("hello"), "color": "default"})
	w.toggle = srv.AddBlock(w.root, "toggle", map[string]any{"rich_text": fakenotion.RichText("more"), "color": "default"})
	srv.AddBlock(w.toggle, "paragraph", map[string]any{"rich_text": fakenotion.RichText("inside"), "color": "default"})
	w.blockChild = srv.AddPage("block_id", w.toggle, "Under a toggle", nil)
	img := srv.AddFile("pic.png", w.imageBytes)
	img["caption"] = []any{}
	w.image = srv.AddBlock(w.root, "image", img)
	srv.AddBlock(w.root, "image", map[string]any{"type": "external", "external": map[string]any{"url": "https://example.com/x.png"}, "caption": []any{}})
	w.child = srv.AddPage("page_id", w.root, "Child", nil)
	srv.AddBlock(w.child, "paragraph", map[string]any{"rich_text": fakenotion.RichText("child text"), "color": "default"})

	w.db = srv.AddDatabase(w.root, "Tasks", map[string]any{
		"Done": map[string]any{"id": "d", "name": "Done", "type": "checkbox", "checkbox": map[string]any{}},
	})
	for i, title := range []string{"first", "second", "third"} {
		id := srv.AddPage("database_id", w.db, title, map[string]any{
			"Done": map[string]any{"id": "d", "type": "checkbox", "checkbox": i%2 == 0},
		})
		w.rows = append(w.rows, id)
	}
	w.rowChild = srv.AddPage("page_id", w.rows[0], "Row child", nil)

	// A page the integration was not invited to: the database and the
	// page under it are shared on their own and must be roots, and their
	// parent must not be followed.
	hidden := srv.AddPage("workspace", "", "Hidden", nil)
	w.rootDB = srv.AddDatabase(hidden, "Standalone", nil)
	w.rootRows = append(w.rootRows, srv.AddPage("database_id", w.rootDB, "lonely row", nil))
	w.orphan = srv.AddPage("page_id", hidden, "Orphan", nil)
	srv.Unshare(hidden)
	return w
}

type result struct {
	infos map[string]objects.FileInfo
	files map[string][]byte
	errs  map[string]error
}

func (r *result) paths() []string {
	var out []string
	for p := range r.infos {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// run drives Import the way kloset does: records are drained, and read,
// concurrently.
func run(t *testing.T, ctx context.Context, imp *Importer) (*result, error) {
	t.Helper()
	records := make(chan *connectors.Record, 4)
	res := &result{infos: map[string]objects.FileInfo{}, files: map[string][]byte{}, errs: map[string]error{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for rec := range records {
				var data []byte
				var rerr error
				if rec.Err == nil && !rec.FileInfo.IsDir() {
					data, rerr = io.ReadAll(rec.Reader)
					_ = rec.Close()
				}
				mu.Lock()
				switch {
				case rec.Err != nil:
					res.errs[rec.Pathname] = rec.Err
				case rerr != nil:
					res.errs[rec.Pathname] = rerr
				default:
					_, dup := res.infos[rec.Pathname]
					require.False(t, dup, "record %s sent twice", rec.Pathname)
					res.infos[rec.Pathname] = rec.FileInfo
					if !rec.FileInfo.IsDir() {
						res.files[rec.Pathname] = data
					}
				}
				mu.Unlock()
			}
		})
	}
	err := imp.Import(ctx, records, nil)
	wg.Wait()
	return res, err
}

func newImp(t *testing.T, srv *fakenotion.Server, location string, extra map[string]string) *Importer {
	t.Helper()
	config := map[string]string{"location": location, "token": fakenotion.Token, "api_url": srv.URL()}
	for k, v := range extra {
		config[k] = v
	}
	imp, err := NewImporter(context.Background(), &connectors.Options{MaxConcurrency: 4}, "notion", config)
	require.NoError(t, err)
	return imp.(*Importer)
}

func requireJSONFile(t *testing.T, res *result, p string, want map[string]any) {
	t.Helper()
	data, ok := res.files[p]
	require.True(t, ok, "missing %s in %v", p, res.paths())
	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	require.JSONEq(t, string(wantJSON), string(data))
	require.Equal(t, int64(len(data)), res.infos[p].Size())
	require.True(t, res.infos[p].Mode().IsRegular())
}

func TestImportWorkspace(t *testing.T) {
	w := newWorkspace(t)
	imp := newImp(t, w.srv, "notion://", nil)
	require.Equal(t, fakenotion.WorkspaceName, imp.Origin())
	require.Equal(t, "notion", imp.Type())
	require.Equal(t, "/", imp.Root())
	require.NoError(t, imp.Ping(context.Background()))

	res, err := run(t, context.Background(), imp)
	require.NoError(t, err)
	require.Empty(t, res.errs)

	root := layout.PageDir("", w.root)
	db := layout.DatabaseDir(root, w.db)
	rootDB := layout.DatabaseDir("", w.rootDB)
	orphan := layout.PageDir("", w.orphan)
	want := []string{
		"/", "/" + layout.WorkspaceFile,
		root, root + "/page.json", root + "/blocks.json",
		root + "/files", root + "/files/" + w.image + ".png",
		root + "/pages",
		layout.PageDir(root, w.blockChild), layout.PageDir(root, w.blockChild) + "/page.json", layout.PageDir(root, w.blockChild) + "/blocks.json",
		layout.PageDir(root, w.child), layout.PageDir(root, w.child) + "/page.json", layout.PageDir(root, w.child) + "/blocks.json",
		root + "/databases",
		db, db + "/database.json", db + "/pages",
		rootDB, rootDB + "/database.json", rootDB + "/pages",
		layout.PageDir(rootDB, w.rootRows[0]), layout.PageDir(rootDB, w.rootRows[0]) + "/page.json", layout.PageDir(rootDB, w.rootRows[0]) + "/blocks.json",
		orphan, orphan + "/page.json", orphan + "/blocks.json",
	}
	for _, row := range w.rows {
		d := layout.PageDir(db, row)
		want = append(want, d, d+"/page.json", d+"/blocks.json")
	}
	rc := layout.PageDir(layout.PageDir(db, w.rows[0]), w.rowChild)
	want = append(want, layout.PageDir(db, w.rows[0])+"/pages", rc, rc+"/page.json", rc+"/blocks.json")
	sort.Strings(want)
	require.Equal(t, want, res.paths())

	// Objects are stored as Notion served them.
	requireJSONFile(t, res, root+"/page.json", w.srv.Page(w.root))
	requireJSONFile(t, res, db+"/database.json", w.srv.Database(w.db))
	requireJSONFile(t, res, layout.PageDir(db, w.rows[1])+"/page.json", w.srv.Page(w.rows[1]))

	// Blocks nest their children, except pages and databases, which are
	// backed up on their own.
	var blocks []map[string]any
	require.NoError(t, json.Unmarshal(res.files[root+"/blocks.json"], &blocks))
	require.Len(t, blocks, 6) // paragraph, toggle, image, external image, child_page, child_database
	require.Equal(t, "toggle", blocks[1]["type"])
	kids := blocks[1]["children"].([]any)
	require.Len(t, kids, 2)
	require.Equal(t, "paragraph", kids[0].(map[string]any)["type"])
	require.Equal(t, "child_page", kids[1].(map[string]any)["type"])
	require.NotContains(t, kids[1], "children")
	require.Equal(t, "child_page", blocks[4]["type"])
	require.NotContains(t, blocks[4], "children")
	require.NotContains(t, blocks[0], "children")
	delete(blocks[1], "children")
	require.Equal(t, w.srv.Block(w.toggle), blocks[1])

	// Hosted files are fetched, external ones are a link in the block.
	require.Equal(t, w.imageBytes, res.files[root+"/files/"+w.image+".png"])
	require.Equal(t, int64(-1), res.infos[root+"/files/"+w.image+".png"].Size())

	for _, p := range []string{"/", root, root + "/files", db + "/pages"} {
		require.True(t, res.infos[p].IsDir(), p)
		require.Equal(t, os.FileMode(0o700), res.infos[p].Mode().Perm(), p)
	}
	require.False(t, res.infos[root+"/page.json"].ModTime().IsZero())

	var ws layout.Workspace
	require.NoError(t, json.Unmarshal(res.files["/"+layout.WorkspaceFile], &ws))
	var ids, titles []string
	for _, r := range ws.Roots {
		ids = append(ids, r.ID)
		titles = append(titles, r.Title)
	}
	require.ElementsMatch(t, []string{w.root, w.rootDB, w.orphan}, ids)
	require.ElementsMatch(t, []string{"Root", "Standalone", "Orphan"}, titles)
	for _, r := range ws.Roots {
		require.Contains(t, res.infos, r.Path)
		if r.ID == w.rootDB {
			require.Equal(t, "database", r.Object)
		}
	}
}

func TestImportIsDeterministic(t *testing.T) {
	w := newWorkspace(t)
	first, err := run(t, context.Background(), newImp(t, w.srv, "notion://", nil))
	require.NoError(t, err)
	second, err := run(t, context.Background(), newImp(t, w.srv, "notion://", nil))
	require.NoError(t, err)
	require.Equal(t, first.paths(), second.paths())
	require.Equal(t, string(first.files["/"+layout.WorkspaceFile]), string(second.files["/"+layout.WorkspaceFile]))
}

func TestImportPaginates(t *testing.T) {
	w := newWorkspace(t)
	w.srv.PageSize = 1
	res, err := run(t, context.Background(), newImp(t, w.srv, "notion://", nil))
	require.NoError(t, err)
	require.Empty(t, res.errs)
	var blocks []map[string]any
	require.NoError(t, json.Unmarshal(res.files[layout.PageDir("", w.root)+"/blocks.json"], &blocks))
	require.Len(t, blocks, 6)
	for _, row := range w.rows {
		require.Contains(t, res.files, layout.PageDir(layout.DatabaseDir(layout.PageDir("", w.root), w.db), row)+"/page.json")
	}
}

func TestImportSubtree(t *testing.T) {
	w := newWorkspace(t)

	t.Run("page", func(t *testing.T) {
		res, err := run(t, context.Background(), newImp(t, w.srv, "notion://"+w.child, nil))
		require.NoError(t, err)
		require.Empty(t, res.errs)
		child := layout.PageDir("", w.child)
		require.Equal(t, []string{"/", child, child + "/blocks.json", child + "/page.json", "/" + layout.WorkspaceFile}, res.paths())
	})

	t.Run("database", func(t *testing.T) {
		bare := strings.ReplaceAll(w.db, "-", "")
		res, err := run(t, context.Background(), newImp(t, w.srv, "notion://www.notion.so/Tasks-"+bare+"?v=1", nil))
		require.NoError(t, err)
		require.Empty(t, res.errs)
		db := layout.DatabaseDir("", w.db)
		require.Contains(t, res.files, db+"/database.json")
		for _, row := range w.rows {
			require.Contains(t, res.files, layout.PageDir(db, row)+"/page.json")
		}
		require.Contains(t, res.files, layout.PageDir(layout.PageDir(db, w.rows[0]), w.rowChild)+"/page.json")
		require.NotContains(t, res.files, layout.PageDir("", w.root)+"/page.json")
	})

	t.Run("unknown", func(t *testing.T) {
		_, err := run(t, context.Background(), newImp(t, w.srv, "notion://00000000-0000-0000-0000-000000000001", nil))
		require.Error(t, err)
		require.True(t, api.IsStatus(err, http.StatusNotFound), err)
	})
}

func TestImportReportsUnreadableObjects(t *testing.T) {
	w := newWorkspace(t)
	w.srv.Fail(w.child, http.StatusNotFound)
	w.srv.Fail(w.rows[2], http.StatusNotFound)

	res, err := run(t, context.Background(), newImp(t, w.srv, "notion://", nil))
	require.NoError(t, err)

	root := layout.PageDir("", w.root)
	childDir := layout.PageDir(root, w.child)
	require.Contains(t, res.errs, childDir)
	require.NotContains(t, res.files, childDir+"/page.json")

	// A row comes whole from the query; only its blocks need a read.
	rowDir := layout.PageDir(layout.DatabaseDir(root, w.db), w.rows[2])
	require.Contains(t, res.files, rowDir+"/page.json")
	require.Contains(t, res.errs, rowDir+"/blocks.json")
	require.NotContains(t, res.files, rowDir+"/blocks.json")

	require.Len(t, res.errs, 2)
	require.Contains(t, res.files, root+"/page.json")
	require.Contains(t, res.files, layout.PageDir("", w.orphan)+"/page.json")
}

func TestImportRefreshesExpiredLinks(t *testing.T) {
	w := newWorkspace(t)
	w.srv.SetField(w.child, "icon", w.srv.AddFile("icon.svg", []byte("<svg/>")))
	w.srv.SetField(w.rootDB, "cover", w.srv.AddFile("cover.jpg", []byte("jpeg")))

	imp := newImp(t, w.srv, "notion://", nil)
	records := make(chan *connectors.Record, 1024)
	require.NoError(t, imp.Import(context.Background(), records, nil))

	// Every link listed so far stops working before it is read.
	w.srv.RotateFile(w.image, "image")
	w.srv.RotateFile(w.child, "icon")
	w.srv.RotateFile(w.rootDB, "cover")

	got := map[string]string{}
	for rec := range records {
		require.NoError(t, rec.Err, rec.Pathname)
		if rec.FileInfo.IsDir() || !strings.Contains(rec.Pathname, "/files/") {
			continue
		}
		data, err := io.ReadAll(rec.Reader)
		require.NoError(t, err, rec.Pathname)
		got[rec.Pathname] = string(data)
	}
	root := layout.PageDir("", w.root)
	require.Equal(t, map[string]string{
		root + "/files/" + w.image + ".png":                   string(w.imageBytes),
		layout.PageDir(root, w.child) + "/files/icon.svg":     "<svg/>",
		layout.DatabaseDir("", w.rootDB) + "/files/cover.jpg": "jpeg",
	}, got)
}

func TestImportSkipAttachments(t *testing.T) {
	w := newWorkspace(t)
	res, err := run(t, context.Background(), newImp(t, w.srv, "notion://", map[string]string{"skip_attachments": "true"}))
	require.NoError(t, err)
	require.Empty(t, res.errs)
	for p := range res.infos {
		require.NotContains(t, p, "/files")
	}
}

func TestImportStopsOnCancel(t *testing.T) {
	w := newWorkspace(t)
	for i := 0; i < 50; i++ {
		w.srv.AddPage("page_id", w.root, "filler", nil)
	}
	imp := newImp(t, w.srv, "notion://", nil)

	ctx, cancel := context.WithCancel(context.Background())
	records := make(chan *connectors.Record)
	done := make(chan error, 1)
	go func() { done <- imp.Import(ctx, records, nil) }()

	<-records // the root directory
	cancel()
	err := <-done
	require.ErrorIs(t, err, context.Canceled)
	_, open := <-records
	require.False(t, open, "records must be closed when Import returns")
}

func TestNewImporterConfig(t *testing.T) {
	srv := fakenotion.New(t)
	ctx := context.Background()
	opts := &connectors.Options{MaxConcurrency: 1}

	for _, tc := range []struct {
		name   string
		config map[string]string
		want   string
	}{
		{name: "no token", config: map[string]string{"location": "notion://"}, want: "missing token"},
		{name: "bad location", config: map[string]string{"location": "notion://nope", "token": fakenotion.Token}, want: "not a notion id"},
		{name: "bad token", config: map[string]string{"location": "notion://", "token": "wrong", "api_url": srv.URL()}, want: "unauthorized"},
		{name: "bad bool", config: map[string]string{"location": "notion://", "token": fakenotion.Token, "api_url": srv.URL(), "skip_attachments": "maybe"}, want: "skip_attachments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewImporter(ctx, opts, "notion", tc.config)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestWithChildren(t *testing.T) {
	kids := []json.RawMessage{json.RawMessage(`{"id":"k"}`)}
	out, err := withChildren(json.RawMessage(`{"id":"b","type":"toggle"}`), kids)
	require.NoError(t, err)
	require.JSONEq(t, `{"id":"b","type":"toggle","children":[{"id":"k"}]}`, string(out))

	out, err = withChildren(json.RawMessage(` { } `), nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"children":[]}`, string(out))

	_, err = withChildren(json.RawMessage(`[1]`), nil)
	require.Error(t, err)
}
