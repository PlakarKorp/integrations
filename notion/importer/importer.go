// Package importer backs up a Notion workspace, or one page or database of
// it, into the tree described by package layout.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/layout"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

// workers bounds the API calls in flight. Notion allows about three
// requests a second per integration; more workers only trade throughput
// for 429s and retries.
const workers = 3

func init() {
	_ = importer.Register("notion", 0, NewImporter)
}

// Importer is the Notion importer.
type Importer struct {
	client          *api.Client
	rootID          string
	origin          string
	skipAttachments bool

	sem chan struct{}

	mu      sync.Mutex
	visited map[string]bool
	// all is every shared object search returned, to find what the
	// walk from the roots did not reach.
	all []*api.Object
}

// NewImporter builds an importer from the connector configuration:
// location (notion:// or notion://<id>), token, and skip_attachments.
func NewImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	token := config["token"]
	if token == "" {
		return nil, errors.New("notion: missing token")
	}
	rootID, err := api.ParseLocation(config["location"])
	if err != nil {
		return nil, fmt.Errorf("notion: %w", err)
	}
	skip := false
	if v, ok := config["skip_attachments"]; ok {
		if skip, err = strconv.ParseBool(v); err != nil {
			return nil, fmt.Errorf("notion: skip_attachments: %w", err)
		}
	}
	// api_url points tests at a fake server; the schema does not admit it.
	return newImporter(ctx, api.NewClient(token, config["api_url"]), rootID, skip)
}

func newImporter(ctx context.Context, client *api.Client, rootID string, skipAttachments bool) (*Importer, error) {
	// One call up front checks the token before a backup is scheduled
	// around it, and names the workspace for the snapshot header.
	me, err := client.Me(ctx)
	if err != nil {
		return nil, fmt.Errorf("notion: %w", err)
	}
	origin := me.Bot.WorkspaceName
	if origin == "" {
		origin = "notion.so"
	}
	return &Importer{
		client:          client,
		rootID:          rootID,
		origin:          origin,
		skipAttachments: skipAttachments,
		sem:             make(chan struct{}, workers),
		visited:         map[string]bool{},
	}, nil
}

// Origin is the name of the workspace.
func (i *Importer) Origin() string { return i.origin }

// Type is the connector name.
func (i *Importer) Type() string { return "notion" }

// Root is where the tree starts.
func (i *Importer) Root() string { return "/" }

// Flags is empty: the importer can run more than once and merges fine.
func (i *Importer) Flags() location.Flags { return 0 }

// Ping checks the token.
func (i *Importer) Ping(ctx context.Context) error { return i.client.Ping(ctx) }

// Close releases nothing: every reader owns its own response.
func (i *Importer) Close(ctx context.Context) error { return nil }

// Import walks the workspace and sends one record per directory and file.
// A page that cannot be read becomes an error record and the walk goes on;
// only a failure to find out what to back up is fatal.
func (i *Importer) Import(ctx context.Context, records chan<- *connectors.Record, _ <-chan *connectors.Result) error {
	defer close(records)

	roots, err := i.roots(ctx)
	if err != nil {
		return err
	}

	e := &emitter{ctx: ctx, records: records}
	e.dir("/", time.Time{})

	var wg sync.WaitGroup
	for _, root := range roots {
		if root.Object == "database" {
			i.walkDatabase(ctx, e, &wg, "", root)
		} else {
			i.walkPage(ctx, e, &wg, "", root)
		}
	}
	wg.Wait()

	// Shared objects the walk did not reach hang under a block whose page
	// is not shared, or under something we failed to read. Back them up as
	// roots rather than lose them.
	if i.rootID == "" {
		var orphans []*api.Object
		i.mu.Lock()
		for _, obj := range i.all {
			if !i.visited[obj.ID] {
				orphans = append(orphans, obj)
			}
		}
		i.mu.Unlock()
		sort.Slice(orphans, func(a, b int) bool { return orphans[a].ID < orphans[b].ID })
		for _, obj := range orphans {
			if obj.Object == "database" {
				i.walkDatabase(ctx, e, &wg, "", obj)
			} else {
				i.walkPage(ctx, e, &wg, "", obj)
			}
		}
		wg.Wait()
		roots = append(roots, orphans...)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	ws := layout.Workspace{Roots: make([]layout.Root, 0, len(roots))}
	for _, r := range roots {
		dir := layout.PageDir("", r.ID)
		if r.Object == "database" {
			dir = layout.DatabaseDir("", r.ID)
		}
		ws.Roots = append(ws.Roots, layout.Root{ID: r.ID, Object: r.Object, Title: r.Title(), Path: dir})
	}
	data, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return fmt.Errorf("notion: encode %s: %w", layout.WorkspaceFile, err)
	}
	e.file("/"+layout.WorkspaceFile, data, time.Time{})
	return nil
}

// roots finds what to back up: the configured object, or every shared
// page and database whose parent is the workspace or is not shared.
func (i *Importer) roots(ctx context.Context) ([]*api.Object, error) {
	if i.rootID != "" {
		obj, err := i.client.Page(ctx, i.rootID)
		if err == nil {
			return []*api.Object{obj}, nil
		}
		if !api.IsStatus(err, http.StatusNotFound) {
			return nil, fmt.Errorf("notion: read root %s: %w", i.rootID, err)
		}
		obj, err = i.client.Database(ctx, i.rootID)
		if err != nil {
			return nil, fmt.Errorf("notion: root %s is neither a page nor a database shared with the integration: %w", i.rootID, err)
		}
		return []*api.Object{obj}, nil
	}

	all, err := i.client.SearchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("notion: list shared pages: %w", err)
	}
	shared := make(map[string]bool, len(all))
	for idx := range all {
		shared[all[idx].ID] = true
	}
	var roots []*api.Object
	for idx := range all {
		obj := &all[idx]
		if obj.Archived || obj.InTrash {
			continue
		}
		i.mu.Lock()
		i.all = append(i.all, obj)
		i.mu.Unlock()
		switch obj.Parent.Type {
		case "workspace":
			roots = append(roots, obj)
		case "page_id", "database_id":
			if !shared[obj.Parent.ID()] {
				roots = append(roots, obj)
			}
		}
	}
	// Search order follows edits; sort so two backups of the same
	// workspace list the same roots in the same order.
	sort.Slice(roots, func(a, b int) bool { return roots[a].ID < roots[b].ID })
	return roots, nil
}

// visit marks an object reached and reports whether it was new. Notion
// trees have no cycles, but a page shared twice over would otherwise be
// backed up twice.
func (i *Importer) visit(id string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.visited[id] {
		return false
	}
	i.visited[id] = true
	return true
}

// acquire takes a worker slot, released by calling the result.
func (i *Importer) acquire(ctx context.Context) (func(), bool) {
	select {
	case <-ctx.Done():
		return nil, false
	case i.sem <- struct{}{}:
		return func() { <-i.sem }, true
	}
}

// walkPage backs up a page and its subtree under parentDir. The object
// may be a bare envelope from a child_page block, in which case the page
// is fetched; a full one from search or a query is used as is.
func (i *Importer) walkPage(ctx context.Context, e *emitter, wg *sync.WaitGroup, parentDir string, page *api.Object) {
	if !i.visit(page.ID) {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		release, ok := i.acquire(ctx)
		if !ok {
			return
		}

		dir := layout.PageDir(parentDir, page.ID)
		if page.Object != "page" {
			full, err := i.client.Page(ctx, page.ID)
			if err != nil {
				release()
				e.err(dir, fmt.Errorf("read page: %w", err))
				return
			}
			page = full
		}

		blocks, children, err := i.blockTree(ctx, page.ID)
		release()

		e.dir(dir, page.LastEditedTime)
		e.file(path.Join(dir, layout.PageFile), page.Raw, page.LastEditedTime)
		if err != nil {
			e.err(path.Join(dir, layout.BlocksFile), fmt.Errorf("read blocks: %w", err))
		} else {
			e.file(path.Join(dir, layout.BlocksFile), blocks, page.LastEditedTime)
		}

		i.attachments(ctx, e, dir, page, children)

		var pages, databases bool
		for _, child := range children {
			switch child.Type {
			case "child_page":
				if !pages {
					pages = true
					e.dir(path.Join(dir, layout.PagesDir), page.LastEditedTime)
				}
				i.walkPage(ctx, e, wg, dir, child)
			case "child_database":
				if !databases {
					databases = true
					e.dir(path.Join(dir, layout.DatabasesDir), page.LastEditedTime)
				}
				i.walkDatabase(ctx, e, wg, dir, child)
			}
		}
	}()
}

// walkDatabase backs up a database, its schema and its rows, under
// parentDir.
func (i *Importer) walkDatabase(ctx context.Context, e *emitter, wg *sync.WaitGroup, parentDir string, db *api.Object) {
	if !i.visit(db.ID) {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		release, ok := i.acquire(ctx)
		if !ok {
			return
		}

		dir := layout.DatabaseDir(parentDir, db.ID)
		if db.Object != "database" {
			full, err := i.client.Database(ctx, db.ID)
			if err != nil {
				release()
				// A linked database shows up as a child_database block
				// but has no object of its own behind it.
				e.err(dir, fmt.Errorf("read database: %w", err))
				return
			}
			db = full
		}

		rows, err := i.client.AllDatabasePages(ctx, db.ID)
		release()

		e.dir(dir, db.LastEditedTime)
		e.file(path.Join(dir, layout.DatabaseFile), db.Raw, db.LastEditedTime)
		i.attachments(ctx, e, dir, db, nil)
		if err != nil {
			e.err(path.Join(dir, layout.PagesDir), fmt.Errorf("query database: %w", err))
			return
		}
		if len(rows) == 0 {
			return
		}
		e.dir(path.Join(dir, layout.PagesDir), db.LastEditedTime)
		for idx := range rows {
			if rows[idx].Archived || rows[idx].InTrash {
				continue
			}
			i.walkPage(ctx, e, wg, dir, &rows[idx])
		}
	}()
}

// blockTree fetches the blocks of a page, children nested under a
// "children" key, and returns the JSON along with the flat list of blocks
// so the caller can find pages, databases and files among them.
// child_page and child_database blocks are not expanded: their content is
// a page or database of its own.
func (i *Importer) blockTree(ctx context.Context, id string) ([]byte, []*api.Object, error) {
	var flat []*api.Object
	tree, err := i.children(ctx, id, &flat, 0)
	if err != nil {
		return nil, nil, err
	}
	data, err := json.MarshalIndent(tree, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode blocks of %s: %w", id, err)
	}
	return data, flat, nil
}

// maxDepth stops a runaway nesting; Notion itself does not go that deep.
const maxDepth = 64

func (i *Importer) children(ctx context.Context, id string, flat *[]*api.Object, depth int) ([]json.RawMessage, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("blocks nested deeper than %d under %s", maxDepth, id)
	}
	blocks, err := i.client.AllBlockChildren(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(blocks))
	for idx := range blocks {
		b := &blocks[idx]
		*flat = append(*flat, b)
		raw := b.Raw
		if b.HasChildren && b.Type != "child_page" && b.Type != "child_database" {
			kids, err := i.children(ctx, b.ID, flat, depth+1)
			if err != nil {
				return nil, err
			}
			raw, err = withChildren(raw, kids)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, raw)
	}
	return out, nil
}

// withChildren adds a "children" key to a block without re-encoding the
// rest of it, so the stored block stays byte for byte what Notion sent.
func withChildren(raw json.RawMessage, kids []json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, errors.New("block is not a JSON object")
	}
	if kids == nil {
		kids = []json.RawMessage{}
	}
	encoded, err := json.Marshal(kids)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Write(trimmed[:len(trimmed)-1])
	if len(bytes.TrimSpace(trimmed[1:len(trimmed)-1])) > 0 {
		buf.WriteByte(',')
	}
	buf.WriteString(`"children":`)
	buf.Write(encoded)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// attachments sends a record for every file Notion hosts for a page or
// database: its icon and cover, and the files of its blocks.
func (i *Importer) attachments(ctx context.Context, e *emitter, dir string, owner *api.Object, blocks []*api.Object) {
	if i.skipAttachments {
		return
	}
	m, err := owner.Map()
	if err != nil {
		e.err(dir, err)
		return
	}

	var files []attachment
	if u := api.HostedFileURL(m["icon"]); u != "" {
		files = append(files, attachment{name: layout.FileName(layout.IconFile, u), url: u, refresh: i.refresher(owner, "icon")})
	}
	if u := api.HostedFileURL(m["cover"]); u != "" {
		files = append(files, attachment{name: layout.FileName(layout.CoverFile, u), url: u, refresh: i.refresher(owner, "cover")})
	}
	for _, b := range blocks {
		if !api.IsFileBlock(b.Type) {
			continue
		}
		bm, err := b.Map()
		if err != nil {
			e.err(path.Join(dir, layout.FilesDir, b.ID), err)
			continue
		}
		u := api.HostedFileURL(bm[b.Type])
		if u == "" {
			continue
		}
		files = append(files, attachment{name: layout.FileName(b.ID, u), url: u, refresh: i.refresher(b, b.Type), modTime: b.LastEditedTime})
	}
	if len(files) == 0 {
		return
	}

	filesDir := path.Join(dir, layout.FilesDir)
	e.dir(filesDir, owner.LastEditedTime)
	for _, f := range files {
		f := f
		if f.modTime.IsZero() {
			f.modTime = owner.LastEditedTime
		}
		e.lazy(path.Join(filesDir, f.name), f.modTime, func() (io.ReadCloser, error) {
			return i.open(ctx, f)
		})
	}
}

type attachment struct {
	name    string
	url     string
	modTime time.Time
	// refresh fetches a fresh download link: Notion signs them for an
	// hour and a long backup reads a record well after it was listed.
	refresh func(context.Context) (string, error)
}

// refresher re-reads the object and returns the current URL of its file
// under key, "icon", "cover" or the block type.
func (i *Importer) refresher(obj *api.Object, key string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var fresh *api.Object
		var err error
		switch obj.Object {
		case "block":
			fresh, err = i.client.Block(ctx, obj.ID)
		case "database":
			fresh, err = i.client.Database(ctx, obj.ID)
		default:
			fresh, err = i.client.Page(ctx, obj.ID)
		}
		if err != nil {
			return "", err
		}
		m, err := fresh.Map()
		if err != nil {
			return "", err
		}
		u := api.HostedFileURL(m[key])
		if u == "" {
			return "", fmt.Errorf("%s %s no longer has a hosted %s", obj.Object, obj.ID, key)
		}
		return u, nil
	}
}

func (i *Importer) open(ctx context.Context, f attachment) (io.ReadCloser, error) {
	resp, err := i.client.FetchAttachment(ctx, f.url)
	if err == nil {
		return resp.Body, nil
	}
	if !api.IsStatus(err, http.StatusForbidden) && !api.IsStatus(err, http.StatusBadRequest) {
		return nil, err
	}
	// An expired link answers 403, sometimes 400. Ask Notion for a new
	// one and try once more.
	fresh, rerr := f.refresh(ctx)
	if rerr != nil {
		return nil, fmt.Errorf("%w (and refreshing the link failed: %v)", err, rerr)
	}
	resp, err = i.client.FetchAttachment(ctx, fresh)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// emitter sends records, dropping them once the context is gone so a
// cancelled backup does not block on a channel nobody reads.
type emitter struct {
	ctx     context.Context
	records chan<- *connectors.Record
}

func (e *emitter) send(r *connectors.Record) {
	select {
	case <-e.ctx.Done():
	case e.records <- r:
	}
}

func (e *emitter) dir(pathname string, modTime time.Time) {
	e.send(connectors.NewRecord(pathname, "", objects.FileInfo{
		Lname:    path.Base(pathname),
		Lmode:    os.ModeDir | 0o700,
		LmodTime: modTime,
	}, nil, nil))
}

func (e *emitter) file(pathname string, data []byte, modTime time.Time) {
	e.send(connectors.NewRecord(pathname, "", objects.FileInfo{
		Lname:    path.Base(pathname),
		Lmode:    0o600,
		Lsize:    int64(len(data)),
		LmodTime: modTime,
	}, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}))
}

// lazy sends a file whose size is unknown until it is read; -1 tells the
// store to compare it without the size.
func (e *emitter) lazy(pathname string, modTime time.Time, open func() (io.ReadCloser, error)) {
	e.send(connectors.NewRecord(pathname, "", objects.FileInfo{
		Lname:    path.Base(pathname),
		Lmode:    0o600,
		Lsize:    -1,
		LmodTime: modTime,
	}, nil, open))
}

func (e *emitter) err(pathname string, err error) {
	e.send(connectors.NewError(pathname, err))
}
