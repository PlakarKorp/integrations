// Package exporter restores a Notion backup, the tree described by package
// layout, under a page of a workspace.
//
// Notion cannot be written in the order records arrive: a page needs its
// parent to exist, its blocks need the whole page, and a row needs its
// database. Records are therefore staged in a private directory first and
// replayed once the last one is in. Every restore creates new pages, the
// API has no way to put an id back.
package exporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/layout"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	_ = exporter.Register("notion", 0, NewExporter)
}

// Exporter is the Notion exporter.
type Exporter struct {
	client          *api.Client
	targetID        string
	origin          string
	skipAttachments bool

	// Records are staged under tmpDir. root confines every write to it,
	// whatever the record pathname says.
	tmpDir string
	root   *os.Root

	// ids maps an id of the backup to the id of what was created for it,
	// so relations and links between restored objects point at the copies.
	ids map[string]string
	// failed maps a staged path to why it, and everything under it, was
	// not restored.
	failed map[string]error
}

// NewExporter builds an exporter from the connector configuration:
// location (notion://<page-id>, the page to restore under), token, and
// skip_attachments.
func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	token := config["token"]
	if token == "" {
		return nil, errors.New("notion: missing token")
	}
	targetID, err := api.ParseLocation(config["location"])
	if err != nil {
		return nil, fmt.Errorf("notion: %w", err)
	}
	if targetID == "" {
		return nil, errors.New("notion: the location must name the page to restore under, notion://<page-id>")
	}
	skip := false
	if v, ok := config["skip_attachments"]; ok {
		if skip, err = strconv.ParseBool(v); err != nil {
			return nil, fmt.Errorf("notion: skip_attachments: %w", err)
		}
	}
	// api_url points tests at a fake server; the schema does not admit it.
	return newExporter(ctx, api.NewClient(token, config["api_url"]), targetID, skip)
}

func newExporter(ctx context.Context, client *api.Client, targetID string, skipAttachments bool) (*Exporter, error) {
	me, err := client.Me(ctx)
	if err != nil {
		return nil, fmt.Errorf("notion: %w", err)
	}
	// Refusing now beats staging a whole snapshot to find out.
	if _, err := client.Page(ctx, targetID); err != nil {
		return nil, fmt.Errorf("notion: target page %s: %w", targetID, err)
	}
	origin := me.Bot.WorkspaceName
	if origin == "" {
		origin = "notion.so"
	}

	tmpDir, err := os.MkdirTemp("", "plakar-notion-restore-*")
	if err != nil {
		return nil, fmt.Errorf("notion: create staging directory: %w", err)
	}
	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("notion: open staging directory: %w", err)
	}
	return &Exporter{
		client:          client,
		targetID:        targetID,
		origin:          origin,
		skipAttachments: skipAttachments,
		tmpDir:          tmpDir,
		root:            root,
		ids:             map[string]string{},
		failed:          map[string]error{},
	}, nil
}

// Origin is the name of the workspace.
func (e *Exporter) Origin() string { return e.origin }

// Type is the connector name.
func (e *Exporter) Type() string { return "notion" }

// Root is the page restored under.
func (e *Exporter) Root() string { return e.targetID }

// Flags is empty: records are accepted in any order.
func (e *Exporter) Flags() location.Flags { return 0 }

// Ping checks the token.
func (e *Exporter) Ping(ctx context.Context) error { return e.client.Ping(ctx) }

// Close removes the staging directory.
func (e *Exporter) Close(ctx context.Context) error {
	err := e.root.Close()
	if rerr := os.RemoveAll(e.tmpDir); err == nil {
		err = rerr
	}
	return err
}

// Export stages every record, replays the tree into Notion, then answers
// each record with what became of it.
func (e *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	var pending []*connectors.Record
	drained := false
	for !drained {
		select {
		case <-ctx.Done():
			drained = true
		case record, ok := <-records:
			if !ok {
				drained = true
				break
			}
			if record.Err != nil || record.IsXattr {
				results <- record.Ok()
				continue
			}
			if err := e.stage(record); err != nil {
				results <- record.Error(err)
				continue
			}
			pending = append(pending, record)
		}
	}

	if err := ctx.Err(); err != nil {
		for _, r := range pending {
			results <- r.Error(err)
		}
		// The producer keeps sending until its own walk notices; drain
		// it so it is not left blocked on the channel.
		for r := range records {
			results <- r.Error(err)
		}
		return err
	}

	replayErr := e.replay(ctx)
	for _, r := range pending {
		if err := e.outcome(r.Pathname); err != nil {
			results <- r.Error(err)
		} else {
			results <- r.Ok()
		}
	}
	return replayErr
}

// relative turns a record pathname into a path under the staging root.
// path.Clean absorbs "..", filepath.Localize rejects what is left that is
// not a plain relative path, and *os.Root keeps symlinks from leading out.
func relative(pathname string) (string, error) {
	p := strings.TrimPrefix(path.Clean("/"+pathname), "/")
	if p == "" {
		return ".", nil
	}
	return filepath.Localize(p)
}

func (e *Exporter) stage(record *connectors.Record) error {
	rel, err := relative(record.Pathname)
	if err != nil {
		return err
	}
	if record.FileInfo.IsDir() {
		if rel == "." {
			return nil
		}
		return e.root.MkdirAll(rel, 0o700)
	}
	if !record.FileInfo.Mode().IsRegular() {
		return fmt.Errorf("%s: a Notion backup holds no %s", record.Pathname, record.FileInfo.Mode().Type())
	}
	// Records of a subtree restore may come before their directory.
	if dir := filepath.Dir(rel); dir != "." {
		if err := e.root.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	f, err := e.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, record.Reader)
	// The record is answered after the replay; its reader is done now.
	_ = record.Close()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stage %s: %w", record.Pathname, err)
	}
	return f.Close()
}

// outcome is why a record was not restored: its own failure, or that of
// the page or database it belongs to.
func (e *Exporter) outcome(pathname string) error {
	p := path.Clean("/" + pathname)
	for {
		if err, ok := e.failed[p]; ok {
			return err
		}
		if p == "/" {
			return nil
		}
		p = path.Dir(p)
	}
}

func (e *Exporter) fail(pathname string, err error) {
	e.failed[pathname] = errors.Join(e.failed[pathname], err)
}

// replay restores every page and database staged, topmost first, under
// the target page. A failure inside one root does not stop the others.
func (e *Exporter) replay(ctx context.Context) error {
	roots, err := e.roots("/")
	if err != nil {
		return fmt.Errorf("notion: read staged tree: %w", err)
	}
	parent := map[string]any{"type": "page_id", "page_id": e.targetID}
	for _, r := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.database {
			e.restoreDatabase(ctx, r.dir, parent)
		} else {
			e.restorePage(ctx, r.dir, parent, nil)
		}
	}
	if err := e.failUnreached("/"); err != nil {
		return fmt.Errorf("notion: read staged tree: %w", err)
	}
	return nil
}

// failUnreached marks every staged page and database the replay did not
// create and did not already account for. A page whose blocks could not
// be read, or one standing where the API takes no page, must not be
// answered as restored.
func (e *Exporter) failUnreached(dir string) error {
	for _, name := range []string{layout.PageFile, layout.DatabaseFile} {
		file := path.Join(dir, name)
		if !e.exists(file) {
			continue
		}
		var obj struct {
			ID string `json:"id"`
		}
		if err := e.loadJSON(file, &obj); err != nil {
			if e.outcome(dir) == nil {
				e.fail(dir, err)
			}
			break
		}
		if _, ok := e.ids[obj.ID]; !ok && e.outcome(dir) == nil {
			e.fail(dir, fmt.Errorf("%s %s was not restored: nothing above it could hold it", strings.TrimSuffix(name, ".json"), obj.ID))
		}
	}
	entries, err := e.readDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := e.failUnreached(path.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

type root struct {
	dir      string
	database bool
}

// roots finds the pages and databases nothing staged above contains. A
// whole backup has them at /pages and /databases; a subtree restore has
// one at /.
func (e *Exporter) roots(dir string) ([]root, error) {
	if e.exists(path.Join(dir, layout.PageFile)) {
		return []root{{dir: dir}}, nil
	}
	if e.exists(path.Join(dir, layout.DatabaseFile)) {
		return []root{{dir: dir, database: true}}, nil
	}
	entries, err := e.readDir(dir)
	if err != nil {
		return nil, err
	}
	var out []root
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sub, err := e.roots(path.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

func (e *Exporter) exists(pathname string) bool {
	rel, err := relative(pathname)
	if err != nil {
		return false
	}
	_, err = e.root.Stat(rel)
	return err == nil
}

func (e *Exporter) readDir(pathname string) ([]fs.DirEntry, error) {
	rel, err := relative(pathname)
	if err != nil {
		return nil, err
	}
	d, err := e.root.Open(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	entries, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Name() < entries[b].Name() })
	return entries, nil
}

func (e *Exporter) open(pathname string) (*os.File, error) {
	rel, err := relative(pathname)
	if err != nil {
		return nil, err
	}
	return e.root.Open(rel)
}

// maxObjectFile caps a staged JSON file. The importer never writes one
// anywhere near it; a restore from a doctored snapshot must not either.
const maxObjectFile = 256 << 20

func (e *Exporter) loadJSON(pathname string, into any) error {
	f, err := e.open(pathname)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxObjectFile+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", pathname, err)
	}
	if len(data) > maxObjectFile {
		return fmt.Errorf("%s is larger than %d bytes", pathname, maxObjectFile)
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("decode %s: %w", pathname, err)
	}
	return nil
}

func (e *Exporter) idmap(old string) (string, bool) {
	id, ok := e.ids[old]
	return id, ok
}

// restorePage creates the page staged in dir under parent, then its
// blocks, child pages and databases. Under a page parent only the title
// survives: a page has no other property there. Under a database the
// properties are kept, less those in drop.
func (e *Exporter) restorePage(ctx context.Context, dir string, parent map[string]any, drop map[string]bool) {
	var page map[string]any
	if err := e.loadJSON(path.Join(dir, layout.PageFile), &page); err != nil {
		e.fail(dir, err)
		return
	}
	oldID, _ := page["id"].(string)

	body, hostedIcon, hostedCover := api.PageForCreate(page, api.PageOptions{
		Parent:    parent,
		TitleOnly: parent["type"] == "page_id",
		Drop:      drop,
		IDs:       e.idmap,
	})
	e.restoreIconAndCover(ctx, dir, body, hostedIcon, hostedCover)

	created, err := e.client.CreatePage(ctx, body)
	if err != nil {
		e.fail(dir, fmt.Errorf("create page %s: %w", oldID, err))
		return
	}
	if oldID != "" {
		e.ids[oldID] = created.ID
	}

	blocksFile := path.Join(dir, layout.BlocksFile)
	if !e.exists(blocksFile) {
		return
	}
	var blocks []map[string]any
	if err := e.loadJSON(blocksFile, &blocks); err != nil {
		e.fail(blocksFile, err)
		return
	}
	e.appendBlocks(ctx, dir, created.ID, created.ID, blocks)
}

// restoreDatabase creates the database staged in dir under parent, then
// its rows, oldest first so they come out in the order they were made.
func (e *Exporter) restoreDatabase(ctx context.Context, dir string, parent map[string]any) {
	var db map[string]any
	if err := e.loadJSON(path.Join(dir, layout.DatabaseFile), &db); err != nil {
		e.fail(dir, err)
		return
	}
	oldID, _ := db["id"].(string)

	body, dropped, hostedIcon, hostedCover := api.DatabaseForCreate(db, parent, e.idmap)
	e.restoreIconAndCover(ctx, dir, body, hostedIcon, hostedCover)

	created, err := e.client.CreateDatabase(ctx, body)
	if err != nil {
		e.fail(dir, fmt.Errorf("create database %s: %w", oldID, err))
		return
	}
	if oldID != "" {
		e.ids[oldID] = created.ID
	}
	if len(dropped) > 0 {
		names := make([]string, 0, len(dropped))
		for name := range dropped {
			names = append(names, name)
		}
		sort.Strings(names)
		e.fail(path.Join(dir, layout.DatabaseFile), fmt.Errorf("properties the API cannot create were left out: %s", strings.Join(names, ", ")))
	}

	rowsDir := path.Join(dir, layout.PagesDir)
	entries, err := e.readDir(rowsDir)
	if err != nil {
		e.fail(rowsDir, err)
		return
	}
	type row struct {
		dir     string
		created string
	}
	rows := make([]row, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rdir := path.Join(rowsDir, entry.Name())
		var page struct {
			CreatedTime string `json:"created_time"`
		}
		if err := e.loadJSON(path.Join(rdir, layout.PageFile), &page); err != nil {
			e.fail(rdir, err)
			continue
		}
		rows = append(rows, row{dir: rdir, created: page.CreatedTime})
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].created < rows[b].created })

	rowParent := map[string]any{"type": "database_id", "database_id": created.ID}
	for _, r := range rows {
		if ctx.Err() != nil {
			e.fail(r.dir, ctx.Err())
			continue
		}
		e.restorePage(ctx, r.dir, rowParent, dropped)
	}
}

// restoreIconAndCover uploads a hosted icon or cover staged under dir and
// points body at the uploads. A failed upload costs the icon, not the page.
func (e *Exporter) restoreIconAndCover(ctx context.Context, dir string, body map[string]any, icon, cover bool) {
	for _, item := range []struct {
		wanted bool
		base   string
		key    string
	}{{icon, layout.IconFile, "icon"}, {cover, layout.CoverFile, "cover"}} {
		if !item.wanted {
			continue
		}
		id, staged, err := e.upload(ctx, dir, item.base)
		if err != nil {
			e.fail(staged, err)
			continue
		}
		body[item.key] = map[string]any{"type": "file_upload", "file_upload": map[string]any{"id": id}}
	}
}

// upload pushes the staged file named base, whatever its extension, from
// the files directory of dir. It returns the upload id and the staged
// path; on failure the path is the file's when one was found, else the
// name it should have had.
func (e *Exporter) upload(ctx context.Context, dir, base string) (id, staged string, err error) {
	filesDir := path.Join(dir, layout.FilesDir)
	staged = path.Join(filesDir, base)
	entries, err := e.readDir(filesDir)
	if err != nil {
		return "", staged, err
	}
	name := ""
	for _, entry := range entries {
		if !entry.IsDir() && layout.StripExtension(entry.Name()) == base {
			name = entry.Name()
			break
		}
	}
	if name == "" {
		return "", staged, fmt.Errorf("hosted file %s was not in the backup", base)
	}
	staged = path.Join(filesDir, name)
	if e.skipAttachments {
		return "", staged, errors.New("attachments skipped by configuration")
	}
	f, err := e.open(staged)
	if err != nil {
		return "", staged, err
	}
	defer func() { _ = f.Close() }()

	id, err = e.client.UploadFile(ctx, name, mime.TypeByExtension(path.Ext(name)), f)
	if err != nil {
		return "", staged, err
	}
	return id, staged, nil
}

// appendBlocks recreates blocks, in order, under parentID. pageID is the
// page they belong to: a child page under a block cannot be created there
// and goes under the page instead. Blocks are sent PageSize at a time;
// a batch Notion refuses is retried one block at a time so one bad block
// costs one block.
func (e *Exporter) appendBlocks(ctx context.Context, dir, pageID, parentID string, blocks []map[string]any) {
	blocksFile := path.Join(dir, layout.BlocksFile)

	type staged struct {
		body     map[string]any
		id       string
		typ      string
		children []map[string]any // appended after creation
		deep     []map[string]any // original children of an inlined block
		objects  []map[string]any // pages and databases found while inlining
	}
	var batch []staged

	flush := func() {
		if len(batch) == 0 {
			return
		}
		bodies := make([]map[string]any, len(batch))
		for k := range batch {
			bodies[k] = batch[k].body
		}
		created, err := e.client.AppendChildren(ctx, parentID, bodies)
		if err != nil && len(batch) > 1 {
			created = created[:0]
			for _, s := range batch {
				one, oerr := e.client.AppendChildren(ctx, parentID, []map[string]any{s.body})
				if oerr != nil {
					e.fail(blocksFile, fmt.Errorf("block %s (%s): %w", s.id, s.typ, oerr))
					created = append(created, api.Object{})
					continue
				}
				created = append(created, one...)
			}
		} else if err != nil {
			e.fail(blocksFile, fmt.Errorf("block %s (%s): %w", batch[0].id, batch[0].typ, err))
			created = []api.Object{{}}
		}
		for k, s := range batch {
			if k >= len(created) || created[k].ID == "" {
				continue
			}
			if len(s.children) > 0 {
				e.appendBlocks(ctx, dir, pageID, created[k].ID, s.children)
			}
			if len(s.deep) > 0 {
				e.appendDeep(ctx, dir, pageID, created[k].ID, s.deep)
			}
		}
		for _, s := range batch {
			e.restoreObjects(ctx, dir, pageID, s.objects)
		}
		batch = batch[:0]
	}

	for _, block := range blocks {
		if err := ctx.Err(); err != nil {
			e.fail(blocksFile, err)
			return
		}
		id, _ := block["id"].(string)
		typ, _ := block["type"].(string)

		if typ == "child_page" || typ == "child_database" {
			flush()
			e.restoreObjects(ctx, dir, pageID, []map[string]any{block})
			continue
		}

		body, ok := e.blockBody(ctx, dir, block)
		if !ok {
			continue
		}
		s := staged{body: body, id: id, typ: typ}
		kids := childBlocks(block)
		switch {
		case api.NeedsInlineChildren(typ):
			// column_list needs its columns and their content in the
			// same request, two levels. What is below that is appended
			// once the created blocks can be listed.
			inlined, objects := e.inline(ctx, dir, kids, 2)
			body[typ].(map[string]any)["children"] = inlined
			s.deep = kids
			s.objects = objects
		case typ == "synced_block" && body["synced_block"].(map[string]any)["synced_from"] != nil:
			// A duplicate shows the original's children, Notion fills
			// them in.
		default:
			s.children = kids
		}
		batch = append(batch, s)
		if len(batch) == api.PageSize {
			flush()
		}
	}
	flush()
}

// blockBody is the body that recreates block, with its hosted file
// uploaded again, or false once the reason is recorded.
func (e *Exporter) blockBody(ctx context.Context, dir string, block map[string]any) (map[string]any, bool) {
	id, _ := block["id"].(string)
	typ, _ := block["type"].(string)
	body, ok := api.BlockForCreate(block, e.idmap)
	if !ok {
		e.fail(path.Join(dir, layout.BlocksFile), fmt.Errorf("block %s: %s blocks cannot be created through the API", id, typ))
		return nil, false
	}
	payload := body[typ].(map[string]any)
	if api.IsFileBlock(typ) && api.IsHostedFile(payload) {
		uploadID, staged, err := e.upload(ctx, dir, id)
		if err != nil {
			e.fail(staged, err)
			e.fail(path.Join(dir, layout.BlocksFile), fmt.Errorf("block %s (%s): %w", id, typ, err))
			return nil, false
		}
		api.SetFileUpload(payload, uploadID)
	}
	return body, true
}

func childBlocks(block map[string]any) []map[string]any {
	raw, _ := block["children"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// restoreObjects restores the pages and databases child_page and
// child_database blocks stand for, under the page they belong to.
func (e *Exporter) restoreObjects(ctx context.Context, dir, pageID string, blocks []map[string]any) {
	parent := map[string]any{"type": "page_id", "page_id": pageID}
	for _, block := range blocks {
		id, _ := block["id"].(string)
		if block["type"] == "child_database" {
			e.restoreDatabase(ctx, layout.DatabaseDir(dir, id), parent)
		} else {
			e.restorePage(ctx, layout.PageDir(dir, id), parent, nil)
		}
	}
}

// inline builds the children array of a block created with its children,
// levels deep. Deeper blocks are left for appendDeep. Pages and databases
// found on the way are returned for the caller to restore once the block
// is in: a column cannot hold a page the API creates, so they go under
// the page, after it.
func (e *Exporter) inline(ctx context.Context, dir string, blocks []map[string]any, levels int) (children []any, objects []map[string]any) {
	children = make([]any, 0, len(blocks))
	for _, block := range blocks {
		typ, _ := block["type"].(string)
		if typ == "child_page" || typ == "child_database" {
			objects = append(objects, block)
			continue
		}
		body, ok := e.blockBody(ctx, dir, block)
		if !ok {
			continue
		}
		if levels > 1 {
			if kids := childBlocks(block); len(kids) > 0 {
				var deeper []map[string]any
				body[typ].(map[string]any)["children"], deeper = e.inline(ctx, dir, kids, levels-1)
				objects = append(objects, deeper...)
			}
		}
		children = append(children, body)
	}
	return children, objects
}

// appendDeep appends, under the blocks created inline two levels under
// createdID, the children those blocks had but could not carry inline.
// The created blocks are listed and matched to the originals by position,
// which holds because inline keeps the order and skips nothing that was
// accepted.
func (e *Exporter) appendDeep(ctx context.Context, dir, pageID, createdID string, originals []map[string]any) {
	e.appendBelow(ctx, dir, pageID, createdID, originals, 2)
}

func (e *Exporter) appendBelow(ctx context.Context, dir, pageID, createdID string, originals []map[string]any, levels int) {
	needed := false
	for _, o := range originals {
		if len(childBlocks(o)) > 0 {
			needed = true
			break
		}
	}
	if !needed {
		return
	}
	created, err := e.client.AllBlockChildren(ctx, createdID)
	if err != nil {
		e.fail(path.Join(dir, layout.BlocksFile), fmt.Errorf("list children of %s: %w", createdID, err))
		return
	}
	k := 0
	for _, o := range originals {
		if _, ok := api.BlockForCreate(o, nil); !ok {
			continue
		}
		if k >= len(created) {
			return
		}
		kids := childBlocks(o)
		if levels > 1 {
			e.appendBelow(ctx, dir, pageID, created[k].ID, kids, levels-1)
		} else if len(kids) > 0 {
			e.appendBlocks(ctx, dir, pageID, created[k].ID, kids)
		}
		k++
	}
}
