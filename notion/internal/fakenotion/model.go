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

// Package fakenotion is an in-memory Notion for the tests: enough of
// the 2026-03-11 API to back up and restore a workspace, with the
// validation rules that matter (read-only fields rejected, media blocks
// wanting uploads, signed urls that expire, listings that paginate).
package fakenotion

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/integrations/notion/api"
)

type object struct {
	kind     string // page, database, data_source, block
	raw      map[string]any
	children []string // blocks of a page or block, data sources of a database, rows of a data source
	parentID string
}

type file struct {
	name        string
	contentType string
	data        []byte
}

type upload struct {
	filename    string
	contentType string
	parts       map[int][]byte
	nparts      int
	status      string
}

// Server is the fake workspace.  Seed it with the Add* helpers, read it
// back with the Get* helpers.
type Server struct {
	mu sync.Mutex

	Token string
	base  string
	srv   *httptest.Server
	now   func() time.Time
	seq   int
	tick  int

	bot      map[string]any
	users    map[string]map[string]any
	userList []string
	objects  map[string]*object
	comments map[string][]map[string]any // parent id -> comments, in order
	files    map[string]*file
	uploads  map[string]*upload
	views    map[string]map[string]any
	viewList []string

	// Requests counts calls per "METHOD /pattern".
	Requests map[string]int
	// FailEvery makes every nth API call answer 429 once.
	FailEvery int
	calls     int
	// Expiry is the validity of signed urls.
	Expiry time.Duration
	// MarkdownLimit truncates markdown after that many blocks (0: never).
	MarkdownLimit int
	// QueryLimit caps a data source query at that many rows (0: never).
	QueryLimit int
}

func newServer() *Server {
	s := &Server{
		Token:    "secret-token",
		now:      time.Now,
		users:    map[string]map[string]any{},
		objects:  map[string]*object{},
		comments: map[string][]map[string]any{},
		files:    map[string]*file{},
		uploads:  map[string]*upload{},
		views:    map[string]map[string]any{},
		Requests: map[string]int{},
		Expiry:   time.Hour,
	}
	s.bot = map[string]any{
		"object": "user", "id": s.newID(), "type": "bot", "name": "plakar",
		"bot": map[string]any{
			"owner":            map[string]any{"type": "workspace", "workspace": true},
			"workspace_name":   "Fake Workspace",
			"workspace_id":     s.newID(),
			"workspace_limits": map[string]any{"max_file_upload_size_in_bytes": 5 << 30},
		},
	}
	return s
}

func (s *Server) newID() string {
	s.seq++
	sum := sha256.Sum256([]byte(fmt.Sprintf("fake-%d", s.seq)))
	return api.ExpandID(hex.EncodeToString(sum[:16]))
}

// stamp hands out creation and edit times: one minute apart, in the
// past, so that rows sort and queries can resume on their creation
// time like in a real workspace.
func (s *Server) stamp() string {
	s.tick++
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(s.tick) * time.Minute).Format("2006-01-02T15:04:05.000Z")
}

func (s *Server) user(id string) map[string]any {
	return map[string]any{"object": "user", "id": id}
}

func (s *Server) actor() string {
	return s.bot["id"].(string)
}

func text(content string) []any {
	return []any{map[string]any{
		"type": "text",
		"text": map[string]any{"content": content, "link": nil},
		"annotations": map[string]any{
			"bold": false, "italic": false, "strikethrough": false,
			"underline": false, "code": false, "color": "default",
		},
		"plain_text": content,
		"href":       nil,
	}}
}

// Text builds a rich text array, as the API would return it.
func Text(content string) []any { return text(content) }

func plain(v any) string {
	arr, _ := v.([]any)
	var b strings.Builder
	for _, it := range arr {
		m, _ := it.(map[string]any)
		if pt, ok := m["plain_text"].(string); ok {
			b.WriteString(pt)
			continue
		}
		if t, ok := m["text"].(map[string]any); ok {
			if c, ok := t["content"].(string); ok {
				b.WriteString(c)
			}
		}
	}
	return b.String()
}

func parentRef(kind, id string) map[string]any {
	if kind == "workspace" {
		return map[string]any{"type": "workspace", "workspace": true}
	}
	return map[string]any{"type": kind, kind: id}
}

// AddUser adds a workspace member.
func (s *Server) AddUser(name, email string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID()
	s.users[id] = map[string]any{
		"object": "user", "id": id, "type": "person", "name": name, "avatar_url": nil,
		"person": map[string]any{"email": email},
	}
	s.userList = append(s.userList, id)
	return id
}

// AddPage creates a page under a page ("page_id"), the workspace
// ("workspace") or a data source ("data_source_id", with properties).
func (s *Server) AddPage(parentKind, parentID, title string, props map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addPage(parentKind, parentID, title, props, "")
}

func (s *Server) addPage(parentKind, parentID, title string, props map[string]any, after string) string {
	id := s.newID()
	if props == nil {
		props = map[string]any{}
	}
	if parentKind != "data_source_id" {
		props = map[string]any{"title": map[string]any{"id": "title", "type": "title", "title": text(title)}}
	} else if title != "" {
		ds := s.objects[parentID]
		for name, p := range ds.raw["properties"].(map[string]any) {
			if p.(map[string]any)["type"] == "title" {
				props[name] = map[string]any{"id": "title", "type": "title", "title": text(title)}
			}
		}
	}
	now := s.stamp()
	parent := parentRef(parentKind, parentID)
	if parentKind == "page_id" {
		if po := s.lookup(parentID); po != nil && po.kind == "block" {
			// a page inside a toggle or a column
			parent = parentRef("block_id", parentID)
		}
	}
	raw := map[string]any{
		"object": "page", "id": id,
		"created_time": now, "last_edited_time": now,
		"created_by": s.user(s.actor()), "last_edited_by": s.user(s.actor()),
		"in_trash": false, "archived": false, "is_locked": false,
		"icon": nil, "cover": nil,
		"parent":     parent,
		"properties": s.fillProperties(parentKind, parentID, props),
		"url":        "https://www.notion.so/" + api.CompactID(id),
		"public_url": nil,
	}
	s.objects[id] = &object{kind: "page", raw: raw, parentID: parentID}
	switch parentKind {
	case "page_id":
		p := s.lookup(parentID)
		b := map[string]any{
			"object": "block", "id": id, "type": "child_page",
			"parent":       parent,
			"created_time": now, "last_edited_time": now,
			"created_by": s.user(s.actor()), "last_edited_by": s.user(s.actor()),
			"has_children": true, "in_trash": false, "archived": false,
			"child_page": map[string]any{"title": title},
		}
		s.objects["block:"+id] = &object{kind: "block", raw: b, parentID: parentID}
		s.insertChild(p, id, after)
		if p.kind == "block" {
			p.raw["has_children"] = true
		}
	case "data_source_id":
		ds := s.objects[parentID]
		ds.children = append(ds.children, id)
	case "database_id":
		db := s.objects[parentID]
		db.children = append(db.children, id)
	}
	return id
}

func (s *Server) insertChild(p *object, id, after string) {
	switch after {
	case "", "end":
		p.children = append(p.children, id)
	case "start":
		p.children = append([]string{id}, p.children...)
	default:
		for i, c := range p.children {
			if c == after {
				p.children = append(p.children[:i+1], append([]string{id}, p.children[i+1:]...)...)
				return
			}
		}
		p.children = append(p.children, id)
	}
}

// fillProperties completes a row's properties with the schema's empty
// values and stamps ids and types, the way Notion returns them.
func (s *Server) fillProperties(parentKind, parentID string, props map[string]any) map[string]any {
	out := map[string]any{}
	if parentKind != "data_source_id" {
		for k, v := range props {
			out[k] = v
		}
		return out
	}
	schema := s.objects[parentID].raw["properties"].(map[string]any)
	for name, sv := range schema {
		sm := sv.(map[string]any)
		typ := sm["type"].(string)
		pv, ok := props[name]
		var val any
		if ok {
			val = pv.(map[string]any)[typ]
		} else {
			val = emptyValue(typ)
		}
		out[name] = map[string]any{"id": sm["id"], "type": typ, typ: val}
	}
	return out
}

func emptyValue(typ string) any {
	switch typ {
	case "title", "rich_text", "multi_select", "people", "files", "relation":
		return []any{}
	case "checkbox":
		return false
	case "formula":
		return map[string]any{"type": "string", "string": nil}
	case "rollup":
		return map[string]any{"type": "array", "array": []any{}, "function": "show_original"}
	case "unique_id":
		return map[string]any{"prefix": nil, "number": 1}
	case "created_time", "last_edited_time":
		return "2026-01-01T00:00:00.000Z"
	case "created_by", "last_edited_by":
		return map[string]any{"object": "user", "id": "00000000-0000-0000-0000-000000000000"}
	case "button":
		return map[string]any{}
	}
	return nil
}

// AddDatabase creates a database with one data source whose schema is
// given as the API returns it (property name -> {type, <type>: {...}}).
func (s *Server) AddDatabase(parentKind, parentID, title string, schema map[string]any) (dbID, dsID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addDatabase(parentKind, parentID, title, schema, false)
}

func (s *Server) addDatabase(parentKind, parentID, title string, schema map[string]any, inline bool) (string, string) {
	id := s.newID()
	now := s.stamp()
	raw := map[string]any{
		"object": "database", "id": id,
		"created_time": now, "last_edited_time": now,
		"created_by": s.user(s.actor()), "last_edited_by": s.user(s.actor()),
		"title":                  text(title),
		"description":            []any{},
		"icon":                   nil,
		"cover":                  nil,
		"parent":                 parentRef(parentKind, parentID),
		"is_inline":              inline,
		"in_trash":               false,
		"archived":               false,
		"is_locked":              false,
		"url":                    "https://www.notion.so/" + api.CompactID(id),
		"public_url":             nil,
		"data_sources":           []any{},
		"show_data_source_title": true,
		"database_type":          nil,
	}
	s.objects[id] = &object{kind: "database", raw: raw, parentID: parentID}
	if parentKind == "page_id" {
		p := s.objects[parentID]
		b := map[string]any{
			"object": "block", "id": id, "type": "child_database",
			"parent":       parentRef("page_id", parentID),
			"created_time": now, "last_edited_time": now,
			"created_by": s.user(s.actor()), "last_edited_by": s.user(s.actor()),
			"has_children": false, "in_trash": false, "archived": false,
			"child_database": map[string]any{"title": title},
		}
		s.objects["block:"+id] = &object{kind: "block", raw: b, parentID: parentID}
		p.children = append(p.children, id)
	}
	dsID := s.addDataSource(id, title, schema)
	s.wireDualRelations(dsID)
	return id, dsID
}

// SetNow replaces the clock, which signs urls and stamps objects.
func (s *Server) SetNow(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// AddDataSource adds another data source to a database.
func (s *Server) AddDataSource(dbID, title string, schema map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addDataSource(dbID, title, schema)
}

func (s *Server) addDataSource(dbID, title string, schema map[string]any) string {
	db := s.objects[dbID]
	id := s.newID()
	now := s.stamp()
	props := map[string]any{}
	hasTitle := false
	for name, v := range schema {
		m := map[string]any{}
		for k, vv := range v.(map[string]any) {
			m[k] = vv
		}
		if m["id"] == nil {
			if m["type"] == "title" {
				m["id"] = "title"
			} else {
				m["id"] = "prop" + strconv.Itoa(s.seq) + "-" + strconv.Itoa(len(props))
			}
		}
		m["name"] = name
		if m["type"] == "title" {
			hasTitle = true
		}
		props[name] = m
	}
	if !hasTitle {
		props["Name"] = map[string]any{"id": "title", "name": "Name", "type": "title", "title": map[string]any{}}
	}
	raw := map[string]any{
		"object": "data_source", "id": id,
		"created_time": now, "last_edited_time": now,
		"created_by": s.user(s.actor()), "last_edited_by": s.user(s.actor()),
		"title":           text(title),
		"description":     []any{},
		"icon":            nil,
		"parent":          parentRef("database_id", dbID),
		"database_parent": db.raw["parent"],
		"properties":      props,
		"in_trash":        false,
		"archived":        false,
		"url":             "https://www.notion.so/" + api.CompactID(id),
		"database_type":   nil,
	}
	s.objects[id] = &object{kind: "data_source", raw: raw, parentID: dbID}
	db.children = append(db.children, id)
	db.raw["data_sources"] = append(db.raw["data_sources"].([]any), map[string]any{"id": id, "name": title})
	return id
}

// AddBlock appends a block to a page or a block.  payload is the
// type-specific object, for instance {"rich_text": Text("hello")}.
func (s *Server) AddBlock(parentID, typ string, payload map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addBlock(parentID, typ, payload, "")
}

func (s *Server) addBlock(parentID, typ string, payload map[string]any, after string) string {
	id := s.newID()
	now := s.stamp()
	parent := s.lookup(parentID)
	pkind := "page_id"
	if parent.kind == "block" {
		pkind = "block_id"
	}
	raw := map[string]any{
		"object": "block", "id": id, "type": typ,
		"parent":       parentRef(pkind, parentID),
		"created_time": now, "last_edited_time": now,
		"created_by": s.user(s.actor()), "last_edited_by": s.user(s.actor()),
		"has_children": false, "in_trash": false, "archived": false,
		typ: payload,
	}
	s.objects["block:"+id] = &object{kind: "block", raw: raw, parentID: parentID}
	s.insertChild(parent, id, after)
	if parent.kind == "block" {
		parent.raw["has_children"] = true
	}
	return id
}

// lookup finds a page, database, data source or block by id.
func (s *Server) lookup(id string) *object {
	if o, ok := s.objects["block:"+id]; ok {
		if p, ok := s.objects[id]; ok && (p.kind == "page" || p.kind == "database") {
			return p
		}
		return o
	}
	return s.objects[id]
}

func (s *Server) block(id string) *object {
	return s.objects["block:"+id]
}

// AddFile stores a file and returns the reference to embed in objects:
// {"type":"file","file":{"url":..., "expiry_time":...}} is rendered
// from it with a fresh signature on every read.
func (s *Server) AddFile(name, contentType string, data []byte) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID()
	s.files[id] = &file{name: name, contentType: contentType, data: data}
	return map[string]any{"type": "file", "file": map[string]any{"url": "notionfile://" + id + "/" + name}}
}

// signed renders a file reference into a url that expires.
func (s *Server) signed(ref string) (string, string) {
	rest := strings.TrimPrefix(ref, "notionfile://")
	exp := s.now().Add(s.Expiry)
	sig := sha256.Sum256([]byte(rest + exp.Format(time.RFC3339Nano)))
	u := fmt.Sprintf("%s/files/%s?exp=%d&sig=%s", s.base, rest, exp.Unix(), hex.EncodeToString(sig[:8]))
	return u, exp.UTC().Format("2006-01-02T15:04:05.000Z")
}

// render walks an object and signs the file references on the way out.
func (s *Server) render(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, c := range t {
			out[k] = s.render(c)
		}
		if f, ok := out["file"].(map[string]any); ok {
			if u, ok := f["url"].(string); ok && strings.HasPrefix(u, "notionfile://") {
				nu, exp := s.signed(u)
				out["file"] = map[string]any{"url": nu, "expiry_time": exp}
			}
		}
		// page objects carry at most 25 relations, the rest is read
		// through the property endpoint
		if out["type"] == "relation" {
			if arr, ok := out["relation"].([]any); ok && len(arr) > 25 {
				out["relation"] = arr[:25]
				out["has_more"] = true
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, c := range t {
			out[i] = s.render(c)
		}
		return out
	}
	return v
}

// AddComment adds a comment on a page or a block; an empty discussion
// starts a new thread.
func (s *Server) AddComment(parentID, discussion, authorID, content string, attachments []map[string]any) (id, discussionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addComment(parentID, discussion, authorID, text(content), attachments, nil)
}

func (s *Server) addComment(parentID, discussion, authorID string, rt []any, attachments []map[string]any, display map[string]any) (string, string) {
	id := s.newID()
	if discussion == "" {
		discussion = s.newID()
	}
	parent := s.lookup(parentID)
	pkind := "block_id"
	if parent != nil && parent.kind == "page" {
		pkind = "page_id"
	}
	if display == nil {
		display = map[string]any{"type": "user", "resolved_name": s.userName(authorID)}
	}
	att := []any{}
	for _, a := range attachments {
		att = append(att, map[string]any{"category": "file", "file": a["file"]})
	}
	now := s.stamp()
	c := map[string]any{
		"object": "comment", "id": id,
		"parent":                   parentRef(pkind, parentID),
		"discussion_id":            discussion,
		"created_time":             now,
		"last_edited_time":         now,
		"created_by":               s.user(authorID),
		"rich_text":                rt,
		"attachments":              att,
		"display_name":             display,
		"has_pending_attachments":  false,
		"original_content_deleted": false,
	}
	s.comments[parentID] = append(s.comments[parentID], c)
	return id, discussion
}

func (s *Server) userName(id string) string {
	if u, ok := s.users[id]; ok {
		return u["name"].(string)
	}
	if id == s.actor() {
		return "plakar"
	}
	return "unknown"
}

// AddView adds a view on a database's data source.
func (s *Server) AddView(dbID, dsID, name, typ string, config map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addView(dbID, dsID, name, typ, nil, nil, config)
}

func (s *Server) addView(dbID, dsID, name, typ string, filter, sorts any, config map[string]any) string {
	id := s.newID()
	now := s.stamp()
	if config == nil {
		config = map[string]any{"type": typ}
	}
	v := map[string]any{
		"object": "view", "id": id,
		"parent":           parentRef("database_id", dbID),
		"data_source_id":   dsID,
		"name":             name,
		"type":             typ,
		"filter":           filter,
		"sorts":            sorts,
		"configuration":    config,
		"created_time":     now,
		"last_edited_time": now,
		"url":              "https://www.notion.so/" + api.CompactID(id),
	}
	s.views[id] = v
	s.viewList = append(s.viewList, id)
	return id
}

// SetIcon / SetCover set a page's or database's icon / cover.
func (s *Server) SetIcon(id string, icon map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[id].raw["icon"] = icon
}

func (s *Server) SetCover(id string, cover map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[id].raw["cover"] = cover
}

// Touch moves an object's last_edited_time forward.
func (s *Server) Touch(id string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[id].raw["last_edited_time"] = at.UTC().Format("2006-01-02T15:04:05.000Z")
}

// --- read-back helpers for the tests ---

// Get returns a page, database, data source or block as the API would.
func (s *Server) Get(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.lookup(id)
	if o == nil {
		return nil
	}
	return s.render(o.raw).(map[string]any)
}

// Children returns the ordered child ids of a page, block, database
// (its data sources) or data source (its rows).
func (s *Server) Children(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.lookup(id)
	if o == nil {
		return nil
	}
	return append([]string(nil), o.children...)
}

// Comments returns the comments on a page or a block.
func (s *Server) Comments(id string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, c := range s.comments[id] {
		out = append(out, s.render(c).(map[string]any))
	}
	return out
}

// Views returns the views of a database.
func (s *Server) Views(dbID string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, id := range s.viewList {
		v := s.views[id]
		if v["parent"].(map[string]any)["database_id"] == dbID {
			out = append(out, v)
		}
	}
	return out
}

// FileData returns the bytes behind a file reference as embedded in an
// object ({"type":"file","file":{"url":...}}), signed or not.
func (s *Server) FileData(ref map[string]any) ([]byte, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, _ := ref["file"].(map[string]any)
	u, _ := f["url"].(string)
	id := s.fileID(u)
	if fl, ok := s.files[id]; ok {
		return fl.data, fl.name
	}
	return nil, ""
}

// fileID finds the file id in a reference, a signed url or a request
// path.
func (s *Server) fileID(u string) string {
	rest := strings.TrimPrefix(u, "notionfile://")
	if strings.HasPrefix(u, "http") || strings.HasPrefix(u, "/") {
		if p, err := url.Parse(u); err == nil {
			rest = strings.TrimPrefix(p.Path, "/files/")
		}
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}

// FullRelation returns every id of a relation property, untruncated.
func (s *Server) FullRelation(pageID, name string) []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.objects[pageID]
	if o == nil {
		return nil
	}
	p, _ := o.raw["properties"].(map[string]any)[name].(map[string]any)
	arr, _ := p["relation"].([]any)
	return append([]any(nil), arr...)
}

// Titles lists "<title>" of the children of a page, in order, for
// assertions on restored structure.
func (s *Server) Titles(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.lookup(id).children {
		switch o := s.lookup(c); o.kind {
		case "page":
			out = append(out, s.titleOf(o))
		case "database":
			out = append(out, plain(o.raw["title"]))
		case "block":
			out = append(out, o.raw["type"].(string))
		}
	}
	return out
}

func (s *Server) titleOf(o *object) string {
	props, _ := o.raw["properties"].(map[string]any)
	for _, p := range props {
		m := p.(map[string]any)
		if m["type"] == "title" {
			return plain(m["title"])
		}
	}
	return ""
}

// Snapshot renders the whole workspace as a comparable structure: for
// each page or database, its title, parent title and content summary.
func (s *Server) Dump() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lines []string
	for id, o := range s.objects {
		if strings.HasPrefix(id, "block:") {
			continue
		}
		switch o.kind {
		case "page":
			lines = append(lines, "page "+s.titleOf(o)+" "+s.blockSummary(o))
		case "database":
			lines = append(lines, "database "+plain(o.raw["title"]))
		case "data_source":
			var props []string
			for name, p := range o.raw["properties"].(map[string]any) {
				props = append(props, name+":"+p.(map[string]any)["type"].(string))
			}
			sort.Strings(props)
			lines = append(lines, "data_source "+plain(o.raw["title"])+" "+strings.Join(props, ","))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func (s *Server) blockSummary(o *object) string {
	var parts []string
	for _, c := range o.children {
		b := s.lookup(c)
		if b.kind != "block" {
			parts = append(parts, b.kind)
			continue
		}
		typ := b.raw["type"].(string)
		payload, _ := b.raw[typ].(map[string]any)
		parts = append(parts, typ+"("+plain(payload["rich_text"])+")")
	}
	return strings.Join(parts, " ")
}
