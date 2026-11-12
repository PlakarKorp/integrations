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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/layout"
	"golang.org/x/sync/errgroup"
)

// entry is an object known to the index.
type entry struct {
	obj      *api.Object
	kind     layout.Entity
	parentID string // the page, database or data source holding it, "" at the top
	orphan   bool   // the parent exists but the token cannot see it

	mu      sync.Mutex
	visited bool
	dir     string // where it was emitted
}

func (e *entry) visit() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.visited {
		return false
	}
	e.visited = true
	return true
}

// index is what search told us about the workspace, with parents
// resolved to a page, database or data source.
type index struct {
	mu       sync.Mutex
	entries  map[string]*entry
	byParent map[string][]*entry
	order    []string // insertion order, for deterministic walks
}

func newIndex() *index {
	return &index{entries: map[string]*entry{}, byParent: map[string][]*entry{}}
}

func (idx *index) get(id string) *entry {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.entries[id]
}

func (idx *index) add(e *entry) *entry {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if cur, ok := idx.entries[e.obj.ID]; ok {
		return cur
	}
	idx.entries[e.obj.ID] = e
	idx.order = append(idx.order, e.obj.ID)
	return e
}

func (idx *index) link(e *entry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if e.parentID != "" {
		idx.byParent[e.parentID] = append(idx.byParent[e.parentID], e)
	}
}

func (idx *index) children(parentID string) []*entry {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return append([]*entry(nil), idx.byParent[parentID]...)
}

func (idx *index) all() []*entry {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	out := make([]*entry, 0, len(idx.order))
	for _, id := range idx.order {
		out = append(out, idx.entries[id])
	}
	return out
}

func entryKind(o *api.Object) layout.Entity {
	switch o.Object {
	case "page":
		return layout.EntityPage
	case "database":
		return layout.EntityDatabase
	case "data_source":
		return layout.EntityDataSource
	}
	return layout.EntityNone
}

// buildIndex lists the pages and data sources search knows, fetches
// their databases, and resolves every parent.
func (p *Importer) buildIndex(ctx context.Context) (*index, error) {
	idx := newIndex()
	collect := func(raw json.RawMessage) error {
		o, err := api.ParseObject(raw)
		if err != nil {
			return nil //nolint:nilerr // a partial object: the walk fetches it in full
		}
		if o.InTrash {
			return nil
		}
		idx.add(&entry{obj: o, kind: entryKind(o)})
		return nil
	}
	if err := p.client.Search(ctx, "page", collect); err != nil {
		return nil, fmt.Errorf("listing pages: %w", err)
	}
	if err := p.client.Search(ctx, "data_source", collect); err != nil {
		return nil, fmt.Errorf("listing data sources: %w", err)
	}

	// data sources hang off databases search does not list
	for _, e := range idx.all() {
		if e.kind != layout.EntityDataSource {
			continue
		}
		dbID := e.obj.Parent.DatabaseID
		if dbID == "" || idx.get(dbID) != nil {
			continue
		}
		if _, _, err := p.fetch(ctx, idx, layout.EntityDatabase, dbID); err != nil {
			return nil, fmt.Errorf("database %s: %w", dbID, err)
		}
	}

	for _, e := range idx.all() {
		if err := p.resolveParent(ctx, idx, e); err != nil {
			return nil, err
		}
	}
	for _, e := range idx.all() {
		idx.link(e)
	}
	return idx, nil
}

// fetch adds an object search did not return.  found is false for an
// object that is missing, hidden from the token or in the trash.
func (p *Importer) fetch(ctx context.Context, idx *index, kind layout.Entity, id string) (e *entry, found bool, err error) {
	if e := idx.get(id); e != nil {
		return e, true, nil
	}
	var raw json.RawMessage
	switch kind {
	case layout.EntityPage:
		raw, err = p.client.Page(ctx, id)
	case layout.EntityDatabase:
		raw, err = p.client.Database(ctx, id)
	case layout.EntityDataSource:
		raw, err = p.client.DataSource(ctx, id)
	default:
		return nil, false, nil
	}
	if err != nil {
		if api.IsNotFound(err) || api.IsForbidden(err) || api.IsValidation(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	o, err := api.ParseObject(raw)
	if err != nil || o.InTrash {
		return nil, false, nil //nolint:nilerr // a partial object is not one we can back up
	}
	e = &entry{obj: o, kind: entryKind(o)}
	if cur := idx.add(e); cur != e {
		// another walker got there first and is resolving it
		return cur, true, nil
	}
	if err := p.resolveParent(ctx, idx, e); err != nil {
		return nil, false, err
	}
	idx.link(e)
	return e, true, nil
}

// resolveParent sets parentID to the page, database or data source
// holding e, fetching objects search missed and climbing out of blocks.
// Parents the token cannot see make e an orphan, walked from the top.
func (p *Importer) resolveParent(ctx context.Context, idx *index, e *entry) error {
	if e.parentID != "" || e.orphan {
		return nil
	}
	parent := e.obj.Parent
	for hops := 0; hops < 16; hops++ {
		switch parent.Type {
		case "workspace", "":
			return nil
		case "page_id", "database_id", "data_source_id":
			kind := map[string]layout.Entity{
				"page_id": layout.EntityPage, "database_id": layout.EntityDatabase, "data_source_id": layout.EntityDataSource,
			}[parent.Type]
			id := parent.ID()
			pe, found, err := p.fetch(ctx, idx, kind, id)
			if err != nil {
				return err
			}
			if !found {
				e.orphan = true
				return nil
			}
			e.parentID = pe.obj.ID
			return nil
		case "block_id":
			raw, err := p.client.Block(ctx, parent.BlockID)
			if err != nil {
				if api.IsNotFound(err) || api.IsForbidden(err) {
					e.orphan = true
					return nil
				}
				return err
			}
			b, err := api.ParseObject(raw)
			if err != nil {
				e.orphan = true
				return nil //nolint:nilerr // a block we cannot read is as good as one we cannot see
			}
			parent = b.Parent
		default:
			e.orphan = true
			return nil
		}
	}
	e.orphan = true
	return nil
}

// walker emits the tree.
type walker struct {
	imp *Importer
	idx *index
	em  *emitter
	g   *errgroup.Group
}

func (w *walker) run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(w.imp.conc)
	w.g = g

	if w.imp.cfg.Root != "" {
		e := w.idx.get(w.imp.cfg.Root)
		if e == nil {
			for _, kind := range []layout.Entity{layout.EntityPage, layout.EntityDatabase, layout.EntityDataSource} {
				var found bool
				var err error
				if e, found, err = w.imp.fetch(gctx, w.idx, kind, w.imp.cfg.Root); err != nil {
					return err
				}
				if found {
					break
				}
			}
		}
		if e == nil {
			return fmt.Errorf("notion: object %s not found or not shared with the integration", w.imp.cfg.Root)
		}
		if e.kind == layout.EntityDataSource {
			// a data source only lives in its database: back that up
			db, found, err := w.imp.fetch(gctx, w.idx, layout.EntityDatabase, e.obj.Parent.DatabaseID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("notion: data source %s: its database %s is not shared with the integration", e.obj.ID, e.obj.Parent.DatabaseID)
			}
			e = db
		}
		w.spawn(gctx, e, "", layout.EntityNone)
		return g.Wait()
	}

	// the top: objects with no parent the token can reach
	for _, e := range w.idx.all() {
		if e.parentID == "" {
			w.spawn(gctx, e, "", layout.EntityNone)
		}
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// whatever search listed that the walk did not reach: a child of
	// something the token sees but did not list it under, or a row the
	// query missed; place it under its parent when that was emitted.
	for {
		progress := false
		g, gctx = errgroup.WithContext(ctx)
		g.SetLimit(w.imp.conc)
		w.g = g
		for _, e := range w.idx.all() {
			e.mu.Lock()
			visited := e.visited
			e.mu.Unlock()
			if visited {
				continue
			}
			parentDir, parentKind := "", layout.EntityNone
			if pe := w.idx.get(e.parentID); pe != nil {
				pe.mu.Lock()
				parentVisited, parentDirDone := pe.visited, pe.dir
				pe.mu.Unlock()
				switch {
				case !parentVisited:
					continue // the parent will be walked first
				case parentDirDone == "":
					// parent excluded: skip the subtree too
					e.mu.Lock()
					e.visited = true
					e.mu.Unlock()
					continue
				default:
					parentDir, parentKind = parentDirDone, pe.kind
				}
			}
			progress = true
			w.spawn(gctx, e, parentDir, parentKind)
		}
		if err := g.Wait(); err != nil {
			return err
		}
		if !progress {
			return nil
		}
	}
}

// spawn walks e in the group when a slot is free, inline otherwise,
// so that walkers waiting on their children never deadlock.
func (w *walker) spawn(ctx context.Context, e *entry, parentDir string, parentKind layout.Entity) {
	if !e.visit() {
		return
	}
	if parentKind == layout.EntityNone && e.kind == layout.EntityDataSource {
		// a data source only ever lives in its database: one whose
		// database the token cannot see is lost, and must be said so
		w.em.err(path.Join("/", layout.DatabasesDir, api.CompactID(e.obj.Parent.DatabaseID)),
			fmt.Errorf("data source %s (%s): its database is not shared with the integration", e.obj.ID, e.obj.TitleText()))
		return
	}
	if ctx.Err() != nil {
		return
	}
	fn := func() error {
		w.walk(ctx, e, parentDir, parentKind)
		return ctx.Err()
	}
	if !w.g.TryGo(fn) {
		_ = fn()
	}
}

func (w *walker) walk(ctx context.Context, e *entry, parentDir string, parentKind layout.Entity) {
	container := layout.ContainerFor(parentDir, parentKind, e.kind)
	dir := path.Join(container, layout.DirName(e.obj.TitleText(), e.obj.ID))
	if w.em.excluded(dir, true) {
		return
	}
	w.em.dir(container, epoch)
	e.mu.Lock()
	e.dir = dir
	e.mu.Unlock()
	w.em.objects.Add(1)

	switch e.kind {
	case layout.EntityPage:
		w.page(ctx, e, dir)
	case layout.EntityDatabase:
		w.database(ctx, e, dir)
	case layout.EntityDataSource:
		w.dataSource(ctx, e, dir)
	default:
	}
}

func (w *walker) page(ctx context.Context, e *entry, dir string) {
	imp := w.imp

	assets := map[string]asset{}
	addRefs := func(raw json.RawMessage) {
		for _, ref := range api.FileRefs(raw) {
			if _, ok := assets[ref.Key]; !ok {
				assets[ref.Key] = asset{key: ref.Key, url: ref.URL}
			}
		}
	}
	addRefs(e.obj.Raw)

	// the page object, with the property values the API truncates
	// read back in full
	about, ok := w.fullObject(ctx, e, dir)
	if !ok {
		return
	}

	// markdown: children and embedded files in one request
	var childPages []string
	var childDBs []layout.DatabaseLink
	md, mdErr := imp.markdown(ctx, e.obj.ID, w.tagFor)
	if mdErr != nil {
		if ctx.Err() != nil {
			return
		}
		w.em.err(path.Join(dir, layout.ContentFile), mdErr)
	} else {
		var files []string
		childPages, childDBs, files = layout.Scan(md)
		for _, u := range files {
			if key, ok := api.AssetKey(u); ok {
				if _, dup := assets[key]; !dup {
					assets[key] = asset{key: key, url: u}
				}
			}
		}
	}

	// comments, eagerly: they are small, and their attachments are assets
	var comments []json.RawMessage
	var tree []map[string]any
	if imp.cfg.Comments != CommentsNone {
		var err error
		if imp.cfg.Comments == CommentsBlocks {
			tree, err = imp.blockTree(ctx, e.obj.ID)
			if err == nil {
				comments, err = w.blockComments(ctx, e.obj.ID, tree)
			}
		} else {
			comments, err = imp.comments(ctx, e.obj.ID)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.em.err(path.Join(dir, layout.CommentsFile), err)
		}
		for _, c := range comments {
			addRefs(c)
		}
	}

	// children: what the markdown says, plus what search listed under
	// us; a child the API cannot hand us is reported under its
	// container, the others are still walked
	var children []*entry
	seen := map[string]struct{}{}
	add := func(e *entry) {
		if e == nil {
			return
		}
		if _, dup := seen[e.obj.ID]; dup {
			return
		}
		seen[e.obj.ID] = struct{}{}
		children = append(children, e)
	}
	for _, id := range childPages {
		ce, found, err := imp.fetch(ctx, w.idx, layout.EntityPage, id)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.em.err(path.Join(dir, layout.PagesDir, api.CompactID(id)), err)
			continue
		}
		if found {
			add(ce)
		}
	}
	for _, link := range childDBs {
		id := link.ID
		if id == "" && link.DataSourceID != "" {
			ds, found, err := imp.fetch(ctx, w.idx, layout.EntityDataSource, link.DataSourceID)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				w.em.err(path.Join(dir, layout.DatabasesDir, api.CompactID(link.DataSourceID)), err)
				continue
			}
			if found {
				id = ds.obj.Parent.DatabaseID
			}
		}
		if id == "" {
			continue
		}
		ce, found, err := imp.fetch(ctx, w.idx, layout.EntityDatabase, id)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.em.err(path.Join(dir, layout.DatabasesDir, api.CompactID(id)), err)
			continue
		}
		if found {
			add(ce)
		}
	}
	for _, ce := range w.idx.children(e.obj.ID) {
		add(ce)
	}
	sort.SliceStable(children, func(i, j int) bool { return children[i].obj.ID < children[j].obj.ID })

	// a page is as new as its newest child: the children's titles are
	// part of its blocks and its markdown
	last := e.obj.LastEditedTime
	for _, ce := range children {
		if ce.obj.LastEditedTime.After(last) {
			last = ce.obj.LastEditedTime
		}
	}
	mtime := imp.mtime(last)

	w.em.dir(dir, mtime)
	w.em.file(path.Join(dir, layout.AboutFile), about, mtime)
	w.assets(e, dir, assets, mtime)

	if tree != nil {
		raw, err := json.MarshalIndent(tree, "", "  ")
		if err == nil {
			w.em.file(path.Join(dir, layout.BlocksFile), raw, mtime)
		}
	} else {
		id := e.obj.ID
		w.em.lazy(path.Join(dir, layout.BlocksFile), mtime, func() (io.ReadCloser, error) {
			raw, err := imp.blocksJSON(imp.bg, id)
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(raw)), nil
		})
	}

	if len(comments) > 0 {
		raw, _ := json.MarshalIndent(comments, "", "  ")
		w.em.file(path.Join(dir, layout.CommentsFile), raw, mtime)
	}

	if mdErr == nil {
		rewritten := layout.Rewrite(md, func(u string) (string, bool) {
			key, ok := api.AssetKey(u)
			if !ok {
				return "", false
			}
			if _, ok := assets[key]; !ok {
				return "", false
			}
			return layout.AssetsDir + "/" + key, true
		})
		w.em.lazy(path.Join(dir, layout.ContentFile), mtime, func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(rewritten))), nil
		})
	}

	for _, ce := range children {
		if ce.kind == layout.EntityDataSource {
			continue
		}
		w.spawn(ctx, ce, dir, layout.EntityPage)
	}
}

// propertyPageSize is how many relations or people a page object
// carries before the rest must be read through the property endpoint.
const propertyPageSize = 25

// fullObject renders the page object for about.json, with the relation
// and people values the API truncates fetched in full.  ok is false
// only when the context is done.
func (w *walker) fullObject(ctx context.Context, e *entry, dir string) ([]byte, bool) {
	var raw map[string]any
	if err := json.Unmarshal(e.obj.Raw, &raw); err != nil {
		return pretty(e.obj.Raw), true
	}
	props, _ := raw["properties"].(map[string]any)
	changed := false
	for name, v := range props {
		pm, _ := v.(map[string]any)
		typ, _ := pm["type"].(string)
		id, _ := pm["id"].(string)
		need := false
		switch typ {
		case "relation":
			need, _ = pm["has_more"].(bool)
		case "people":
			arr, _ := pm["people"].([]any)
			need = len(arr) >= propertyPageSize
		}
		if !need || id == "" {
			continue
		}
		var items []any
		err := w.imp.client.PropertyItems(ctx, e.obj.ID, id, func(item json.RawMessage) error {
			var it map[string]any
			if err := json.Unmarshal(item, &it); err != nil {
				return fmt.Errorf("decoding property item: %w", err)
			}
			if v, ok := it[typ]; ok && v != nil {
				items = append(items, v)
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, false
			}
			w.em.err(path.Join(dir, "properties", name), fmt.Errorf("reading the full value: %w", err))
			continue
		}
		if items == nil {
			items = []any{}
		}
		pm[typ] = items
		delete(pm, "has_more")
		changed = true
	}
	if !changed {
		return pretty(e.obj.Raw), true
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return pretty(e.obj.Raw), true
	}
	return out, true
}

// tagFor names a page or a database the markdown left out, the way
// the markdown itself links them.
func (w *walker) tagFor(id string) (string, bool) {
	e := w.idx.get(id)
	if e == nil {
		return "", false
	}
	title := strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;").Replace(e.obj.TitleText())
	switch e.kind {
	case layout.EntityPage:
		return fmt.Sprintf(`<page url="https://www.notion.so/%s">%s</page>`, api.CompactID(id), title), true
	case layout.EntityDatabase:
		ds := ""
		if len(e.obj.DataSources) > 0 {
			ds = fmt.Sprintf(` data-source-url="https://www.notion.so/%s"`, api.CompactID(e.obj.DataSources[0].ID))
		}
		return fmt.Sprintf(`<database url="https://www.notion.so/%s"%s>%s</database>`, api.CompactID(id), ds, title), true
	default:
		return "", false
	}
}

func (w *walker) blockComments(ctx context.Context, pageID string, tree []map[string]any) ([]json.RawMessage, error) {
	comments, err := w.imp.comments(ctx, pageID)
	if err != nil {
		return nil, err
	}
	var firstErr error
	walkTree(tree, func(b map[string]any) {
		if firstErr != nil {
			return
		}
		id, _ := b["id"].(string)
		typ, _ := b["type"].(string)
		if id == "" || noDescent[typ] {
			return
		}
		more, err := w.imp.comments(ctx, id)
		if err != nil {
			firstErr = err
			return
		}
		comments = append(comments, more...)
	})
	return comments, firstErr
}

func (w *walker) assets(e *entry, dir string, assets map[string]asset, mtime time.Time) {
	if len(assets) == 0 {
		return
	}
	keys := make([]string, 0, len(assets))
	for k := range assets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	base := path.Join(dir, layout.AssetsDir)
	w.em.dir(base, mtime)
	dirs := map[string]struct{}{}
	for _, k := range keys {
		a := assets[k]
		sub := path.Join(base, path.Dir(k))
		if _, ok := dirs[sub]; !ok {
			dirs[sub] = struct{}{}
			w.em.dir(sub, mtime)
		}
		w.em.lazy(path.Join(base, k), mtime, w.imp.openAsset(e, a))
	}
}

func (w *walker) database(ctx context.Context, e *entry, dir string) {
	imp := w.imp
	mtime := imp.mtime(e.obj.LastEditedTime)
	w.em.dir(dir, mtime)
	w.em.file(path.Join(dir, layout.AboutFile), pretty(e.obj.Raw), mtime)

	assets := map[string]asset{}
	for _, ref := range api.FileRefs(e.obj.Raw) {
		assets[ref.Key] = asset{key: ref.Key, url: ref.URL}
	}
	w.assets(e, dir, assets, mtime)

	// pages parented by the database itself (wikis)
	for _, ce := range w.idx.children(e.obj.ID) {
		if ce.kind == layout.EntityPage {
			w.spawn(ctx, ce, dir, layout.EntityDatabase)
		}
	}

	for _, ref := range e.obj.DataSources {
		ds, found, err := imp.fetch(ctx, w.idx, layout.EntityDataSource, ref.ID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.em.err(path.Join(dir, layout.SourcesDir, api.CompactID(ref.ID)), err)
			continue
		}
		if !found {
			w.em.err(path.Join(dir, layout.SourcesDir, api.CompactID(ref.ID)), fmt.Errorf("data source %s not accessible", ref.ID))
			continue
		}
		w.spawn(ctx, ds, dir, layout.EntityDatabase)
	}

	if imp.cfg.Views {
		if err := w.views(ctx, e, dir); err != nil {
			if ctx.Err() != nil {
				return
			}
			w.em.err(path.Join(dir, layout.ViewsFile), err)
		}
	}
}

func (w *walker) views(ctx context.Context, e *entry, dir string) error {
	var refs []string
	err := w.imp.client.Views(ctx, e.obj.ID, func(raw json.RawMessage) error {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &v) == nil && v.ID != "" {
			refs = append(refs, v.ID)
		}
		return nil
	})
	if err != nil {
		if api.IsNotFound(err) || api.IsValidation(err) {
			return nil // views are not available for every database
		}
		return err
	}
	var views []json.RawMessage
	for _, id := range refs {
		raw, err := w.imp.client.View(ctx, id)
		if err != nil {
			if api.IsNotFound(err) || api.IsForbidden(err) {
				continue
			}
			return err
		}
		views = append(views, raw)
	}
	if len(views) == 0 {
		return nil
	}
	raw, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		return err
	}
	w.em.file(path.Join(dir, layout.ViewsFile), raw, w.imp.mtime(e.obj.LastEditedTime))
	return nil
}

func (w *walker) dataSource(ctx context.Context, e *entry, dir string) {
	imp := w.imp
	mtime := imp.mtime(e.obj.LastEditedTime)
	w.em.dir(dir, mtime)
	w.em.file(path.Join(dir, layout.AboutFile), pretty(e.obj.Raw), mtime)

	err := imp.client.QueryDataSource(ctx, e.obj.ID, func(raw json.RawMessage) error {
		o, err := api.ParseObject(raw)
		if err != nil || o.InTrash {
			return nil //nolint:nilerr // a partial row is listed by search and fetched then
		}
		row := w.idx.add(&entry{obj: o, kind: layout.EntityPage, parentID: e.obj.ID})
		w.spawn(ctx, row, dir, layout.EntityDataSource)
		return nil
	})
	if err != nil && ctx.Err() == nil {
		w.em.err(path.Join(dir, layout.RowsDir), err)
	}
}

func pretty(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return raw
	}
	return buf.Bytes()
}
