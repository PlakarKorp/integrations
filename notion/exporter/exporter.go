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

// Package exporter restores a Notion backup under a page of a workspace.
//
// Notion assigns new ids to everything it creates, so a restore builds
// copies: pages, databases, data sources, rows, blocks, files, comments
// and views are recreated under the target page, in their original
// order, and every reference between them (parents, relations,
// mentions, links, synced blocks) is rewritten to the copy.  Restoring
// the same snapshot twice yields two copies.
//
// The records arrive in the lexical order of the snapshot, which the
// layout designed so that an object comes before its files, its files
// before its blocks, its blocks before its comments, and all of it
// before its children.
package exporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/layout"
)

const (
	maxConcurrency = 4
	maxJSON        = 64 << 20
)

// Config is where to restore and what to recreate.
type Config struct {
	Parent   string // the page to restore under
	Comments bool
	Views    bool
}

// Exporter restores a backup under a page.
type Exporter struct {
	opts   *integration.Options
	client *api.Client
	cfg    Config
	warn   io.Writer

	// open entity directories, innermost last
	stack []*entity

	// old id -> new id
	pages       map[string]string
	databases   map[string]string
	dataSources map[string]string
	blocks      map[string]string

	// new data source id -> what we know of its schema
	schemas map[string]*schema

	// work that needs every object to exist, run at the end in this order
	relations []func(context.Context) error
	rollups   []func(context.Context) error
	formulas  []func(context.Context) error
	values    []func(context.Context) error
	views     []func(context.Context) error
	// references to objects restored later in the walk, patched last
	links []func(context.Context) error

	// pendingRefs counts, while a block or a page is being prepared,
	// the references to objects not restored yet
	pendingRefs int

	// users of the target workspace, nil until needed, empty when
	// the token cannot list them
	users map[string]struct{}

	// errors that could not be tied to a record
	failures int
}

// ParseLocation reads the target page out of notion://<page id>.
func ParseLocation(name, loc string) (string, error) {
	rest := strings.TrimPrefix(loc, name+"://")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", errors.New("notion: a restore needs a target page: notion://<page id>")
	}
	if id := api.IDFromURL("https://www.notion.so/" + rest); id != "" {
		return id, nil
	}
	if api.IsID(rest) {
		return api.ExpandID(rest), nil
	}
	return "", fmt.Errorf("invalid notion location %q: expected notion://<page id>", loc)
}

func boolOpt(config map[string]string, key string, def bool) (bool, error) {
	v, ok := config[key]
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("invalid value for %s: %w", key, err)
	}
	return b, nil
}

// NewExporter is the plugin entry point: it reads the configuration
// plakar passes and builds the exporter.
func NewExporter(ctx context.Context, opts *integration.Options, name string, config map[string]string) (integration.Exporter, error) {
	parent, err := ParseLocation(name, config["location"])
	if err != nil {
		return nil, err
	}
	cfg := Config{Parent: parent}
	if cfg.Comments, err = boolOpt(config, "comments", true); err != nil {
		return nil, err
	}
	if cfg.Views, err = boolOpt(config, "views", true); err != nil {
		return nil, err
	}
	rate := 0.0
	if v := config["rate"]; v != "" {
		if rate, err = strconv.ParseFloat(v, 64); err != nil || rate <= 0 {
			return nil, fmt.Errorf("invalid value for rate: %q (requests per second)", v)
		}
	}
	tok := config["token"]
	if tok == "" {
		tok = os.Getenv("NOTION_TOKEN")
	}
	client, err := api.New(api.Config{
		Token:       tok,
		BaseURL:     config["api_url"],
		Rate:        rate,
		Concurrency: min(max(opts.MaxConcurrency, 1), maxConcurrency),
	})
	if err != nil {
		return nil, err
	}
	return New(opts, client, cfg), nil
}

// New builds an exporter on a client.
func New(opts *integration.Options, client *api.Client, cfg Config) *Exporter {
	var warn io.Writer = os.Stderr
	if opts != nil && opts.Stderr != nil {
		warn = opts.Stderr
	}
	return &Exporter{
		opts:        opts,
		client:      client,
		cfg:         cfg,
		warn:        warn,
		pages:       map[string]string{},
		databases:   map[string]string{},
		dataSources: map[string]string{},
		blocks:      map[string]string{},
		schemas:     map[string]*schema{},
	}
}

func (p *Exporter) Origin() string              { return "notion" }
func (p *Exporter) Type() string                { return "notion" }
func (p *Exporter) Root() string                { return "/" }
func (p *Exporter) Flags() location.Flags       { return location.FLAG_WALKDIRORDER }
func (p *Exporter) Close(context.Context) error { return nil }

func (p *Exporter) Ping(ctx context.Context) error {
	_, err := p.client.Page(ctx, p.cfg.Parent)
	if err != nil {
		return fmt.Errorf("notion: target page %s: %w", p.cfg.Parent, err)
	}
	return nil
}

// warnf reports something the restore lost; the restore as a whole
// then fails at the end.
func (p *Exporter) warnf(format string, args ...any) {
	p.failures++
	p.notef(format, args...)
}

// notef reports something no restore could do, which is not a failure.
func (p *Exporter) notef(format string, args ...any) {
	_, _ = fmt.Fprintf(p.warn, "notion: "+format+"\n", args...)
}

func (p *Exporter) Export(ctx context.Context, records <-chan *integration.Record, results chan<- *integration.Result) error {
	defer close(results)

	if err := p.Ping(ctx); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case record, ok := <-records:
			if !ok {
				return p.finish(ctx)
			}
			if record.Err != nil || record.IsXattr {
				results <- record.Ok()
				continue
			}
			if err := p.handle(ctx, record); err != nil {
				if ctx.Err() != nil {
					_ = record.Close()
					return ctx.Err()
				}
				results <- record.Error(err)
			} else {
				results <- record.Ok()
			}
		}
	}
}

// finish closes what is still open and runs the deferred work.
func (p *Exporter) finish(ctx context.Context) error {
	for len(p.stack) > 0 {
		if err := p.pop(ctx); err != nil {
			p.warnf("%v", err)
		}
	}
	for _, phase := range [][]func(context.Context) error{p.relations, p.rollups, p.formulas, p.values, p.views, p.links} {
		for _, fn := range phase {
			if err := fn(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				p.warnf("%v", err)
			}
		}
	}
	// what could not be tied to a record was only warned about so
	// far; the restore as a whole must not look clean
	if p.failures > 0 {
		return fmt.Errorf("notion: %d objects not restored, see the warnings", p.failures)
	}
	return nil
}

// entity is an object being restored: its directory in the snapshot
// and what has been created for it so far.
type entity struct {
	dir    string
	kind   layout.Entity
	oldID  string
	newID  string
	parent *entity
	obj    *api.Object
	err    error // why it could not be created

	// uploads of the assets under the directory, by asset key
	uploads map[string]upload
	// whether page-level fields wait for the assets
	patch bool

	// pages: the top-level blocks in their original order and where
	// each created block landed, for positions and comments
	siblings []string
	blockMap map[string]string

	// databases: the database is created with its first data source
	// when that is known; otherwise with a bare data source, which the
	// first one to arrive takes over
	created       bool
	placeholderDS string
}

type upload struct {
	id   string
	name string
}

func (e *entity) ready() error {
	if e.err != nil {
		return e.err
	}
	if e.newID == "" {
		return fmt.Errorf("%s: not created", e.dir)
	}
	return nil
}

func (p *Exporter) top() *entity {
	if len(p.stack) == 0 {
		return nil
	}
	return p.stack[len(p.stack)-1]
}

func (p *Exporter) find(dir string) *entity {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].dir == dir {
			return p.stack[i]
		}
	}
	return nil
}

func within(pathname, dir string) bool {
	return pathname == dir || strings.HasPrefix(pathname, dir+"/")
}

// closeUntil pops the entities the record is not part of.
func (p *Exporter) closeUntil(ctx context.Context, pathname string) error {
	for len(p.stack) > 0 && !within(pathname, p.top().dir) {
		if err := p.pop(ctx); err != nil {
			p.warnf("%v", err)
		}
	}
	return nil
}

func (p *Exporter) pop(ctx context.Context) error {
	e := p.top()
	p.stack = p.stack[:len(p.stack)-1]
	return p.close(ctx, e)
}

// close finishes an entity: a database never created because it had
// no data source gets created now, pending page patches are applied.
func (p *Exporter) close(ctx context.Context, e *entity) error {
	if e.err != nil {
		return nil //nolint:nilerr // already reported on the record that failed
	}
	if e.kind == layout.EntityDatabase && !e.created && e.obj != nil {
		if err := p.createDatabase(ctx, e, nil); err != nil {
			return err
		}
	}
	return p.flushAssets(ctx, e)
}

// open pushes an entity for a directory, creating entries for parents
// whose directory record did not come through (a partial restore).
func (p *Exporter) open(c layout.Path) *entity {
	if e := p.find(c.Entity); e != nil {
		return e
	}
	var parent *entity
	if c.ParentDir != "" {
		parent = p.find(c.ParentDir)
		if parent == nil {
			// a partial restore: the ancestors are not part of
			// it, their subtree is grafted under the target
			pc := layout.Classify(c.ParentDir)
			if pc.Kind == layout.KindEntity {
				parent = p.open(pc)
			}
		}
	}
	e := &entity{
		dir:      c.Entity,
		kind:     c.Object,
		oldID:    c.ID,
		parent:   parent,
		uploads:  map[string]upload{},
		blockMap: map[string]string{},
	}
	p.stack = append(p.stack, e)
	return e
}

func readJSON(r io.Reader, v any) error {
	raw, err := io.ReadAll(io.LimitReader(r, maxJSON+1))
	if err != nil {
		return err
	}
	if len(raw) > maxJSON {
		return fmt.Errorf("file larger than %d bytes", maxJSON)
	}
	return json.Unmarshal(raw, v)
}

func (p *Exporter) handle(ctx context.Context, record *integration.Record) error {
	defer func() { _ = record.Close() }()

	c := layout.Classify(record.Pathname)
	if err := p.closeUntil(ctx, record.Pathname); err != nil {
		return err
	}

	switch c.Kind {
	case layout.KindRoot, layout.KindRootFile, layout.KindContainer, layout.KindAssetsDir, layout.KindContent:
		return nil
	case layout.KindUnknown:
		return fmt.Errorf("%s: not a path of a notion backup", record.Pathname)
	case layout.KindEntity:
		p.open(c)
		return nil
	default:
		// a file of the object, handled below
	}

	e := p.open(c)
	if e.err != nil {
		return e.err
	}

	switch c.Kind {
	case layout.KindAbout:
		var raw json.RawMessage
		if err := readJSON(record.Reader, &raw); err != nil {
			e.err = fmt.Errorf("%s: %w", record.Pathname, err)
			return e.err
		}
		obj, err := api.ParseObject(raw)
		if err != nil {
			e.err = fmt.Errorf("%s: %w", record.Pathname, err)
			return e.err
		}
		e.obj = obj
		if err := p.create(ctx, e); err != nil {
			e.err = err
			return err
		}
		return nil

	case layout.KindAsset:
		return p.uploadAsset(ctx, e, c.AssetKey, record)

	case layout.KindBlocks:
		if err := e.ready(); err != nil {
			return err
		}
		if err := p.flushAssets(ctx, e); err != nil {
			return err
		}
		var tree []map[string]any
		if err := readJSON(record.Reader, &tree); err != nil {
			return fmt.Errorf("%s: %w", record.Pathname, err)
		}
		return p.restoreBlocks(ctx, e, tree)

	case layout.KindComments:
		if !p.cfg.Comments {
			return nil
		}
		if err := e.ready(); err != nil {
			return err
		}
		var comments []map[string]any
		if err := readJSON(record.Reader, &comments); err != nil {
			return fmt.Errorf("%s: %w", record.Pathname, err)
		}
		return p.restoreComments(ctx, e, comments)

	case layout.KindViews:
		if !p.cfg.Views {
			return nil
		}
		if err := e.ready(); err != nil {
			return err
		}
		if err := p.flushAssets(ctx, e); err != nil {
			return err
		}
		var views []map[string]any
		if err := readJSON(record.Reader, &views); err != nil {
			return fmt.Errorf("%s: %w", record.Pathname, err)
		}
		// views refer to properties the schema phases add at the end
		p.views = append(p.views, func(ctx context.Context) error {
			return p.restoreViews(ctx, e, views)
		})
		return nil
	default:
		return nil
	}
}

// create makes the object of an entity once its about.json is read.
func (p *Exporter) create(ctx context.Context, e *entity) error {
	switch e.kind {
	case layout.EntityPage:
		return p.createPage(ctx, e)
	case layout.EntityDatabase:
		// created with its first data source, so that the schema
		// is right from the start; see close for the other case
		return nil
	case layout.EntityDataSource:
		return p.createDataSource(ctx, e)
	default:
		return fmt.Errorf("%s: unknown object kind", e.dir)
	}
}

// parentRef is the parent to create an entity under: the copy of its
// parent, or the target page when the parent is not being restored.
func (p *Exporter) parentRef(ctx context.Context, e *entity) (map[string]any, *entity, error) {
	pe := e.parent
	for pe != nil && pe.obj == nil && pe.err == nil {
		pe = pe.parent
	}
	if pe == nil {
		return map[string]any{"type": "page_id", "page_id": p.cfg.Parent}, nil, nil
	}
	if pe.kind == layout.EntityDatabase && !pe.created && pe.err == nil && pe.obj != nil {
		// a page or data source needs its database to exist first
		if err := p.createDatabase(ctx, pe, nil); err != nil {
			return nil, nil, err
		}
	}
	if err := pe.ready(); err != nil {
		return nil, nil, fmt.Errorf("%s: parent: %w", e.dir, err)
	}
	switch pe.kind {
	case layout.EntityPage:
		return map[string]any{"type": "page_id", "page_id": pe.newID}, pe, nil
	case layout.EntityDatabase:
		return map[string]any{"type": "database_id", "database_id": pe.newID}, pe, nil
	case layout.EntityDataSource:
		return map[string]any{"type": "data_source_id", "data_source_id": pe.newID}, pe, nil
	default:
		return nil, nil, fmt.Errorf("%s: bad parent kind", e.dir)
	}
}

// --- assets ---

func (p *Exporter) uploadAsset(ctx context.Context, e *entity, key string, record *integration.Record) error {
	if record.Reader == nil {
		return fmt.Errorf("%s: no content", record.Pathname)
	}
	size := record.FileInfo.Size()
	name := record.FileInfo.Name()
	if name == "" {
		name = "file"
	}
	id, err := p.client.Upload(ctx, name, "", size, record.Reader)
	if err != nil {
		return fmt.Errorf("%s: %w", record.Pathname, err)
	}
	e.uploads[key] = upload{id: id, name: name}
	e.patch = true
	return nil
}

// fileRef turns a file object of the backup into something writable:
// external urls as they are, Notion-hosted files through their upload.
func (p *Exporter) fileRef(e *entity, v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	switch m["type"] {
	case "external":
		ext, _ := m["external"].(map[string]any)
		u, _ := ext["url"].(string)
		if u == "" {
			return nil, false
		}
		return map[string]any{"type": "external", "external": map[string]any{"url": u}}, true
	case "file":
		f, _ := m["file"].(map[string]any)
		u, _ := f["url"].(string)
		key, ok := api.AssetKey(u)
		if !ok {
			return nil, false
		}
		up, ok := e.uploads[key]
		if !ok {
			return nil, false
		}
		return api.FileUploadRef(up.id), true
	case "file_upload":
		return m, true
	}
	return nil, false
}

// flushAssets applies the page-level fields that waited for uploads:
// a file icon, a file cover, files properties.
func (p *Exporter) flushAssets(ctx context.Context, e *entity) error {
	if !e.patch || e.obj == nil || e.newID == "" {
		e.patch = false
		return nil
	}
	e.patch = false

	var raw map[string]any
	if err := json.Unmarshal(e.obj.Raw, &raw); err != nil {
		return err
	}
	body := map[string]any{}
	if icon, ok := raw["icon"].(map[string]any); ok && icon["type"] == "file" {
		if ref, ok := p.fileRef(e, icon); ok {
			body["icon"] = ref
		}
	}
	if cover, ok := raw["cover"].(map[string]any); ok && cover["type"] == "file" {
		if ref, ok := p.fileRef(e, cover); ok {
			body["cover"] = ref
		}
	}
	if e.kind == layout.EntityPage {
		props := map[string]any{}
		for name, pv := range e.obj.Properties {
			var v struct {
				Type  string `json:"type"`
				Files []any  `json:"files"`
			}
			if json.Unmarshal(pv, &v) != nil || v.Type != "files" || len(v.Files) == 0 {
				continue
			}
			if e.parent == nil || e.parent.kind != layout.EntityDataSource {
				continue
			}
			var files []any
			for _, f := range v.Files {
				ref, ok := p.fileRef(e, f)
				if !ok {
					continue
				}
				if fm, ok := f.(map[string]any); ok {
					if n, ok := fm["name"].(string); ok && n != "" {
						ref["name"] = n
					}
				}
				files = append(files, ref)
			}
			if len(files) > 0 {
				props[name] = map[string]any{"files": files}
			}
		}
		if len(props) > 0 {
			body["properties"] = props
		}
	}
	if len(body) == 0 {
		return nil
	}
	var err error
	switch e.kind {
	case layout.EntityPage:
		_, err = p.client.UpdatePage(ctx, e.newID, body)
	case layout.EntityDatabase:
		err = p.updateDatabase(ctx, e.newID, body)
	default:
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: applying files: %w", e.dir, err)
	}
	return nil
}

func (p *Exporter) updateDatabase(ctx context.Context, id string, body map[string]any) error {
	delete(body, "properties")
	if len(body) == 0 {
		return nil
	}
	return p.client.UpdateDatabase(ctx, id, body)
}

// --- pages ---

func (p *Exporter) createPage(ctx context.Context, e *entity) error {
	parent, pe, err := p.parentRef(ctx, e)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(e.obj.Raw, &raw); err != nil {
		return err
	}

	body := map[string]any{"parent": parent}

	props := map[string]any{}
	var dropped []string
	before := p.pendingRefs
	if pe != nil && pe.kind == layout.EntityDataSource {
		props, dropped = p.rowProperties(ctx, e, pe)
	} else {
		title := e.obj.TitlePropertyName()
		if title != "" {
			var v struct {
				Title []any `json:"title"`
			}
			_ = json.Unmarshal(e.obj.Properties[title], &v)
			props["title"] = map[string]any{"title": p.richText(v.Title)}
		}
	}
	body["properties"] = props
	if p.pendingRefs > before {
		p.links = append(p.links, func(ctx context.Context) error {
			return p.patchTexts(ctx, e, pe)
		})
	}

	if icon, ok := raw["icon"].(map[string]any); ok {
		if icon["type"] == "file" {
			e.patch = true
		} else if ref := iconRef(icon); ref != nil {
			body["icon"] = ref
		}
	}
	if cover, ok := raw["cover"].(map[string]any); ok {
		if cover["type"] == "file" {
			e.patch = true
		} else if ref, ok := p.fileRef(e, cover); ok {
			body["cover"] = ref
		}
	}
	if pe != nil && pe.kind == layout.EntityPage {
		if pos := p.position(pe, e.oldID); pos != nil {
			body["position"] = pos
		}
	}

	resp, err := p.client.CreatePage(ctx, body)
	if err != nil && api.IsValidation(err) && len(props) > 1 {
		// something in the properties is not accepted: keep the page
		p.warnf("%s: properties not restored: %v", e.dir, err)
		title := map[string]any{}
		for name, v := range props {
			if m, ok := v.(map[string]any); ok {
				if _, ok := m["title"]; ok {
					title[name] = v
				}
			}
		}
		body["properties"] = title
		resp, err = p.client.CreatePage(ctx, body)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", e.dir, err)
	}
	created, err := api.ParseObject(resp)
	if err != nil {
		return fmt.Errorf("%s: %w", e.dir, err)
	}
	e.newID = created.ID
	p.pages[e.oldID] = created.ID
	// a child_page block has the id of its page
	p.blocks[e.oldID] = created.ID
	if pe != nil {
		pe.blockMap[e.oldID] = created.ID
	}
	for _, d := range dropped {
		p.warnf("%s: property %q not restored", e.dir, d)
	}
	return nil
}

// patchTexts writes the title and text properties of a page again at
// the end, when the objects they mention have been restored.
func (p *Exporter) patchTexts(ctx context.Context, e, pe *entity) error {
	if e.newID == "" {
		return nil
	}
	props := map[string]any{}
	for name, raw := range e.obj.Properties {
		var v struct {
			Type string `json:"type"`
			Text []any  `json:"rich_text"`
			Head []any  `json:"title"`
		}
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		key := name
		if pe == nil || pe.kind != layout.EntityDataSource {
			if v.Type != "title" {
				continue
			}
			key = "title"
		}
		switch v.Type {
		case "title":
			props[key] = map[string]any{"title": p.richText(v.Head)}
		case "rich_text":
			props[key] = map[string]any{"rich_text": p.richText(v.Text)}
		}
	}
	if len(props) == 0 {
		return nil
	}
	if _, err := p.client.UpdatePage(ctx, e.newID, map[string]any{"properties": props}); err != nil {
		return fmt.Errorf("%s: texts not updated with the restored references: %w", e.dir, err)
	}
	return nil
}

// position places a child page where it was among its parent's blocks.
func (p *Exporter) position(parent *entity, oldID string) map[string]any {
	idx := -1
	for i, id := range parent.siblings {
		if id == oldID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	for i := idx - 1; i >= 0; i-- {
		if newID, ok := parent.blockMap[parent.siblings[i]]; ok {
			return map[string]any{"type": "after_block", "after_block": map[string]any{"id": newID}}
		}
	}
	return map[string]any{"type": "page_start"}
}

func iconRef(icon map[string]any) map[string]any {
	switch icon["type"] {
	case "emoji":
		return map[string]any{"type": "emoji", "emoji": icon["emoji"]}
	case "external":
		return map[string]any{"type": "external", "external": icon["external"]}
	case "custom_emoji":
		ce, _ := icon["custom_emoji"].(map[string]any)
		if id, ok := ce["id"]; ok {
			return map[string]any{"type": "custom_emoji", "custom_emoji": map[string]any{"id": id}}
		}
	case "icon":
		return map[string]any{"type": "icon", "icon": icon["icon"]}
	}
	return nil
}

// --- users ---

// knownUser tells whether a user exists in the target workspace, so
// that people properties do not make a whole page fail.
func (p *Exporter) knownUser(ctx context.Context, id string) bool {
	if p.users == nil {
		p.users = map[string]struct{}{}
		err := p.client.Users(ctx, func(raw json.RawMessage) error {
			var u struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &u) == nil {
				p.users[u.ID] = struct{}{}
			}
			return nil
		})
		if err != nil && !api.IsForbidden(err) {
			p.warnf("listing users: %v", err)
		}
	}
	_, ok := p.users[id]
	return ok
}
