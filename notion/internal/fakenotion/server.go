// Package fakenotion is an in-memory Notion workspace behind an httptest
// server. It answers the read endpoints the importer uses and the write
// endpoints the exporter uses, and refuses the read-only fields the real API
// refuses, so a sanitizer bug fails a test instead of a restore.
package fakenotion

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Token is the only bearer token the server accepts.
const Token = "ntn_fake_token"

// WorkspaceName is what /users/me reports as the workspace.
const WorkspaceName = "Fake Workspace"

type object struct {
	raw      map[string]any
	children []string // block ids, for pages and blocks
}

// Server is one fake workspace.
type Server struct {
	t  *testing.T
	ts *httptest.Server

	mu        sync.Mutex
	pages     map[string]*object
	databases map[string]*object
	blocks    map[string]*object
	order     []string // search order of pages and databases
	files     map[string][]byte
	uploads   map[string][]byte
	seq       int

	// PageSize is how many results a list endpoint returns per call.
	PageSize int
	// rateLimitLeft is how many of the next requests answer 429, those
	// whose path ends with rateLimitSuffix when it is set.
	rateLimitLeft   int
	rateLimitSuffix string
	// serverErrorsLeft is how many of the next requests answer 503.
	serverErrorsLeft int
	// failing maps an object id to the status its reads answer with.
	failing  map[string]int
	requests int
}

// New starts a server torn down with the test.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		t:         t,
		pages:     map[string]*object{},
		databases: map[string]*object{},
		blocks:    map[string]*object{},
		files:     map[string][]byte{},
		uploads:   map[string][]byte{},
		failing:   map[string]int{},
		PageSize:  100,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/files/", s.serveFile)
	mux.HandleFunc("/v1/", s.serveAPI)
	s.ts = httptest.NewServer(mux)
	t.Cleanup(s.ts.Close)
	return s
}

// URL is the API base URL to hand the client.
func (s *Server) URL() string { return s.ts.URL + "/v1" }

// Requests is how many API calls were made.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// RateLimitNext makes the next n requests answer 429 with a Retry-After.
func (s *Server) RateLimitNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rateLimitLeft = n
	s.rateLimitSuffix = ""
}

// RateLimitPath is RateLimitNext for the requests whose path ends with
// suffix only.
func (s *Server) RateLimitPath(suffix string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rateLimitLeft = n
	s.rateLimitSuffix = suffix
}

// ServerErrorNext makes the next n requests answer 503.
func (s *Server) ServerErrorNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serverErrorsLeft = n
}

// Fail makes every read of the object with this id answer status.
func (s *Server) Fail(id string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing[id] = status
}

func (s *Server) newID() string {
	s.seq++
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("fakenotion-%d", s.seq))).String()
}

func (s *Server) now() string {
	return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(s.seq) * time.Minute).Format(time.RFC3339Nano)
}

var user = map[string]any{"object": "user", "id": "11111111-2222-3333-4444-555555555555"}

func richText(text string) []any {
	return []any{map[string]any{
		"type":        "text",
		"text":        map[string]any{"content": text, "link": nil},
		"annotations": map[string]any{"bold": false, "italic": false, "strikethrough": false, "underline": false, "code": false, "color": "default"},
		"plain_text":  text,
		"href":        nil,
	}}
}

// RichText is a one-item rich text array as the API shapes it.
func RichText(text string) []any { return richText(text) }

func (s *Server) stamp(m map[string]any) {
	now := s.now()
	m["created_time"] = now
	m["last_edited_time"] = now
	m["created_by"] = user
	m["last_edited_by"] = user
	m["archived"] = false
	m["in_trash"] = false
}

// Parent builds a parent object.
func Parent(kind, id string) map[string]any {
	if kind == "workspace" {
		return map[string]any{"type": "workspace", "workspace": true}
	}
	return map[string]any{"type": kind, kind: id}
}

// AddPage adds a page with a title and extra properties, returning its id.
// The parent kind is page_id, database_id, block_id or workspace.
func (s *Server) AddPage(parentKind, parentID, title string, props map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID()
	properties := map[string]any{}
	for k, v := range props {
		properties[k] = v
	}
	titleKey := "title"
	if parentKind == "database_id" {
		titleKey = "Name"
	}
	properties[titleKey] = map[string]any{"id": "title", "type": "title", "title": richText(title)}
	raw := map[string]any{
		"object":     "page",
		"id":         id,
		"parent":     Parent(parentKind, parentID),
		"properties": properties,
		"icon":       nil,
		"cover":      nil,
		"url":        "https://www.notion.so/" + strings.ReplaceAll(id, "-", ""),
		"public_url": nil,
	}
	s.stamp(raw)
	s.pages[id] = &object{raw: raw}
	s.order = append(s.order, id)
	if parentKind == "page_id" || parentKind == "block_id" {
		s.appendBlockLocked(parentID, map[string]any{
			"type":       "child_page",
			"child_page": map[string]any{"title": title},
		}, id)
	}
	return id
}

// Unshare hides a page or database from the integration: search no longer
// lists it and reading it answers 404, as Notion does for a page the
// integration was not invited to.
func (s *Server) Unshare(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pages, id)
	delete(s.databases, id)
	for i, o := range s.order {
		if o == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// SetField sets a top-level field of a page or database, icon or cover say.
func (s *Server) SetField(id, key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.lookup(id); o != nil {
		o.raw[key] = value
	}
}

// AddDatabase adds a database under a page with the given schema.
func (s *Server) AddDatabase(parentPageID, title string, schema map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID()
	props := map[string]any{"Name": map[string]any{"id": "title", "type": "title", "name": "Name", "title": map[string]any{}}}
	for k, v := range schema {
		props[k] = v
	}
	raw := map[string]any{
		"object":      "database",
		"id":          id,
		"parent":      Parent("page_id", parentPageID),
		"title":       richText(title),
		"description": []any{},
		"properties":  props,
		"is_inline":   false,
		"icon":        nil,
		"cover":       nil,
		"url":         "https://www.notion.so/" + strings.ReplaceAll(id, "-", ""),
		"public_url":  nil,
	}
	s.stamp(raw)
	s.databases[id] = &object{raw: raw}
	s.order = append(s.order, id)
	s.appendBlockLocked(parentPageID, map[string]any{
		"type":           "child_database",
		"child_database": map[string]any{"title": title},
	}, id)
	return id
}

// AddBlock appends a block of the given type and payload under a page or
// block and returns its id.
func (s *Server) AddBlock(parentID, typ string, payload map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendBlockLocked(parentID, map[string]any{"type": typ, typ: payload}, "")
}

// AddFile hosts an attachment and returns a file object pointing at it.
func (s *Server) AddFile(name string, data []byte) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[name] = data
	return map[string]any{
		"type": "file",
		"file": map[string]any{
			"url":         s.ts.URL + "/files/" + name,
			"expiry_time": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
}

func (s *Server) appendBlockLocked(parentID string, block map[string]any, id string) string {
	if id == "" {
		id = s.newID()
	}
	// A page and the child_page block standing for it share an id;
	// blocks appended to that id belong to the page.
	parentKind := "page_id"
	parent := s.pages[parentID]
	if parent == nil {
		parentKind = "block_id"
		parent = s.blocks[parentID]
	}
	if parent == nil {
		s.t.Fatalf("fakenotion: append to unknown parent %s", parentID)
	}
	block["object"] = "block"
	block["id"] = id
	block["parent"] = Parent(parentKind, parentID)
	block["has_children"] = false
	s.stamp(block)
	if _, exists := s.blocks[id]; !exists {
		s.blocks[id] = &object{raw: block}
	}
	parent.children = append(parent.children, id)
	if parentKind == "block_id" {
		parent.raw["has_children"] = true
	}
	return id
}

func (s *Server) lookup(id string) *object {
	if o := s.pages[id]; o != nil {
		return o
	}
	if o := s.databases[id]; o != nil {
		return o
	}
	return s.blocks[id]
}

// Page returns a copy of a page as the API would serve it, nil if unknown.
func (s *Server) Page(id string) map[string]any { return s.snapshot(s.pages, id) }

// Database returns a copy of a database, nil if unknown.
func (s *Server) Database(id string) map[string]any { return s.snapshot(s.databases, id) }

// Block returns a copy of a block, nil if unknown.
func (s *Server) Block(id string) map[string]any { return s.snapshot(s.blocks, id) }

func (s *Server) snapshot(from map[string]*object, id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := from[id]
	if o == nil {
		return nil
	}
	return clone(o.raw)
}

// Children returns copies of the direct child blocks of a page or block.
func (s *Server) Children(id string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.lookup(id)
	if o == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(o.children))
	for _, cid := range o.children {
		out = append(out, clone(s.blocks[cid].raw))
	}
	return out
}

// Rows returns copies of the pages of a database.
func (s *Server) Rows(databaseID string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, id := range s.order {
		if p := s.pages[id]; p != nil && parentID(p.raw) == databaseID {
			out = append(out, clone(p.raw))
		}
	}
	return out
}

// Upload returns the bytes of a finished upload.
func (s *Server) Upload(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.uploads[id]
}

func clone(m map[string]any) map[string]any {
	data, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

func parentID(raw map[string]any) string {
	p, _ := raw["parent"].(map[string]any)
	kind, _ := p["type"].(string)
	id, _ := p[kind].(string)
	return id
}

// --- HTTP

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.message }

func fail(status int, code, format string, args ...any) *apiError {
	return &apiError{status: status, code: code, message: fmt.Sprintf(format, args...)}
}

// RotateFile gives the hosted file of a block, or the icon or cover of a
// page or database, a new URL, the way a signed link stops working after
// an hour. The old URL answers 403 from then on.
func (s *Server) RotateFile(id, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.lookup(id)
	if o == nil {
		s.t.Fatalf("fakenotion: rotate file of unknown object %s", id)
	}
	holder, _ := o.raw[key].(map[string]any)
	f, _ := holder["file"].(map[string]any)
	u, _ := f["url"].(string)
	name := strings.TrimPrefix(u, s.ts.URL+"/files/")
	data, ok := s.files[name]
	if !ok {
		s.t.Fatalf("fakenotion: %s.%s is not a hosted file", id, key)
	}
	delete(s.files, name)
	s.seq++
	fresh := fmt.Sprintf("%s-v%d", name, s.seq)
	s.files[fresh] = data
	f["url"] = s.ts.URL + "/files/" + fresh
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data, ok := s.files[strings.TrimPrefix(r.URL.Path, "/files/")]
	s.mu.Unlock()
	if !ok {
		// S3 answers an expired signature with 403, not 404.
		http.Error(w, "AccessDenied", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++

	if s.rateLimitLeft > 0 && strings.HasSuffix(r.URL.Path, s.rateLimitSuffix) {
		s.rateLimitLeft--
		w.Header().Set("Retry-After", "0")
		writeErr(w, fail(http.StatusTooManyRequests, "rate_limited", "slow down"))
		return
	}
	if s.serverErrorsLeft > 0 {
		s.serverErrorsLeft--
		writeErr(w, fail(http.StatusServiceUnavailable, "service_unavailable", "try later"))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+Token {
		writeErr(w, fail(http.StatusUnauthorized, "unauthorized", "bad token"))
		return
	}
	if r.Header.Get("Notion-Version") == "" {
		writeErr(w, fail(http.StatusBadRequest, "missing_version", "Notion-Version header required"))
		return
	}

	out, err := s.route(r)
	if err != nil {
		var ae *apiError
		if e, ok := err.(*apiError); ok {
			ae = e
		} else {
			ae = fail(http.StatusBadRequest, "validation_error", "%v", err)
		}
		writeErr(w, ae)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func writeErr(w http.ResponseWriter, e *apiError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "error", "status": e.status, "code": e.code, "message": e.message})
}

func (s *Server) route(r *http.Request) (any, error) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/"), "/"), "/")
	switch {
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "users" && parts[1] == "me":
		return map[string]any{"object": "user", "id": user["id"], "type": "bot", "name": "plakar", "bot": map[string]any{"workspace_name": WorkspaceName}}, nil
	case r.Method == http.MethodPost && len(parts) == 1 && parts[0] == "search":
		return s.search(r)
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "pages":
		return s.read(s.pages, parts[1])
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "databases":
		return s.read(s.databases, parts[1])
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "databases" && parts[2] == "query":
		return s.query(r, parts[1])
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "blocks":
		return s.read(s.blocks, parts[1])
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "blocks" && parts[2] == "children":
		return s.children(r, parts[1])
	case r.Method == http.MethodPatch && len(parts) == 3 && parts[0] == "blocks" && parts[2] == "children":
		return s.appendChildren(r, parts[1])
	case r.Method == http.MethodPost && len(parts) == 1 && parts[0] == "pages":
		return s.createPage(r)
	case r.Method == http.MethodPost && len(parts) == 1 && parts[0] == "databases":
		return s.createDatabase(r)
	case r.Method == http.MethodPost && len(parts) == 1 && parts[0] == "file_uploads":
		return s.createUpload(r)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "file_uploads" && parts[2] == "send":
		return s.sendUpload(r, parts[1])
	}
	return nil, fail(http.StatusNotFound, "invalid_request_url", "%s %s", r.Method, r.URL.Path)
}

func decode(r *http.Request, into any) error {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, into)
}

func (s *Server) read(from map[string]*object, id string) (any, error) {
	if status, ok := s.failing[id]; ok {
		return nil, fail(status, "injected", "object %s is failing", id)
	}
	o := from[id]
	if o == nil {
		return nil, fail(http.StatusNotFound, "object_not_found", "could not find %s", id)
	}
	return o.raw, nil
}

// paginate serves ids[start:start+PageSize] resolved through from. Pages
// and the child_page blocks standing for them share an id, so the caller
// says which table the ids name.
func (s *Server) paginate(r *http.Request, ids []string, from map[string]*object, cursorFromBody bool) (any, error) {
	start := 0
	cursor := r.URL.Query().Get("start_cursor")
	if cursorFromBody {
		var body struct {
			StartCursor string `json:"start_cursor"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		cursor = body.StartCursor
	}
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil {
			return nil, fail(http.StatusBadRequest, "validation_error", "bad cursor")
		}
		start = n
	}
	end := start + s.PageSize
	if end > len(ids) {
		end = len(ids)
	}
	if start > end {
		start = end
	}
	results := make([]any, 0, end-start)
	for _, id := range ids[start:end] {
		o := from[id]
		if o == nil {
			o = s.lookup(id)
		}
		results = append(results, o.raw)
	}
	out := map[string]any{"object": "list", "results": results, "has_more": end < len(ids), "next_cursor": nil}
	if end < len(ids) {
		out["next_cursor"] = strconv.Itoa(end)
	}
	return out, nil
}

func (s *Server) search(r *http.Request) (any, error) {
	return s.paginate(r, s.order, nil, true)
}

func (s *Server) query(r *http.Request, dbID string) (any, error) {
	if _, err := s.read(s.databases, dbID); err != nil {
		return nil, err
	}
	var rows []string
	for _, id := range s.order {
		if p := s.pages[id]; p != nil && parentID(p.raw) == dbID {
			rows = append(rows, id)
		}
	}
	return s.paginate(r, rows, s.pages, true)
}

func (s *Server) children(r *http.Request, id string) (any, error) {
	if status, ok := s.failing[id]; ok {
		return nil, fail(status, "injected", "object %s is failing", id)
	}
	o := s.lookup(id)
	if o == nil || s.databases[id] != nil {
		return nil, fail(http.StatusNotFound, "object_not_found", "could not find block %s", id)
	}
	return s.paginate(r, o.children, s.blocks, false)
}

// blockTypes are the block types the API knows.
var blockTypes = map[string]bool{
	"paragraph": true, "heading_1": true, "heading_2": true, "heading_3": true,
	"bulleted_list_item": true, "numbered_list_item": true, "to_do": true, "toggle": true,
	"quote": true, "callout": true, "code": true, "divider": true, "equation": true,
	"image": true, "video": true, "audio": true, "file": true, "pdf": true,
	"bookmark": true, "embed": true, "link_to_page": true, "link_preview": true,
	"table": true, "table_row": true, "column_list": true, "column": true,
	"synced_block": true, "table_of_contents": true, "breadcrumb": true,
	"child_page": true, "child_database": true, "unsupported": true, "template": true,
}

// readOnly lists the keys a create body must not carry.
var readOnly = []string{"id", "created_time", "last_edited_time", "created_by", "last_edited_by", "url", "public_url", "archived", "in_trash", "has_children", "request_id"}

func rejectReadOnly(where string, m map[string]any) error {
	for _, k := range readOnly {
		if _, ok := m[k]; ok {
			return fail(http.StatusBadRequest, "validation_error", "%s.%s should be not present", where, k)
		}
	}
	return nil
}

// checkRichText refuses derived rich text fields anywhere in a value.
func checkRichText(where string, v any) error {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t["annotations"]; ok {
			for _, k := range []string{"plain_text", "href"} {
				if _, ok := t[k]; ok {
					return fail(http.StatusBadRequest, "validation_error", "%s.%s should be not present", where, k)
				}
			}
		}
		if t["object"] == "user" && len(t) > 2 {
			return fail(http.StatusBadRequest, "validation_error", "%s: user objects take only an id", where)
		}
		for k, val := range t {
			if err := checkRichText(where+"."+k, val); err != nil {
				return err
			}
		}
	case []any:
		for i, val := range t {
			if err := checkRichText(fmt.Sprintf("%s[%d]", where, i), val); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) checkFileObject(where string, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	switch m["type"] {
	case "file":
		return fail(http.StatusBadRequest, "validation_error", "%s: hosted files cannot be sent back", where)
	case "file_upload":
		fu, _ := m["file_upload"].(map[string]any)
		id, _ := fu["id"].(string)
		if _, ok := s.uploads[id]; !ok {
			return fail(http.StatusBadRequest, "validation_error", "%s: unknown file upload %q", where, id)
		}
	}
	return nil
}

func (s *Server) createPage(r *http.Request) (any, error) {
	var body map[string]any
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if err := rejectReadOnly("body", body); err != nil {
		return nil, err
	}
	if err := checkRichText("body", body); err != nil {
		return nil, err
	}
	parent, _ := body["parent"].(map[string]any)
	kind, _ := parent["type"].(string)
	pid, _ := parent[kind].(string)
	props, _ := body["properties"].(map[string]any)
	switch kind {
	case "page_id":
		if s.pages[pid] == nil {
			return nil, fail(http.StatusNotFound, "object_not_found", "parent page %s", pid)
		}
		for name := range props {
			if name != "title" {
				return nil, fail(http.StatusBadRequest, "validation_error", "body.properties.%s is not a property of a page", name)
			}
		}
	case "database_id":
		db := s.databases[pid]
		if db == nil {
			return nil, fail(http.StatusNotFound, "object_not_found", "parent database %s", pid)
		}
		schema, _ := db.raw["properties"].(map[string]any)
		for name, v := range props {
			def, ok := schema[name].(map[string]any)
			if !ok {
				return nil, fail(http.StatusBadRequest, "validation_error", "body.properties.%s is not a property of the database", name)
			}
			pv, _ := v.(map[string]any)
			if _, ok := pv[def["type"].(string)]; !ok {
				return nil, fail(http.StatusBadRequest, "validation_error", "body.properties.%s expects a %s", name, def["type"])
			}
			if _, ok := pv["id"]; ok {
				return nil, fail(http.StatusBadRequest, "validation_error", "body.properties.%s.id should be not present", name)
			}
		}
	default:
		return nil, fail(http.StatusBadRequest, "validation_error", "body.parent must be a page_id or database_id")
	}
	for _, k := range []string{"icon", "cover"} {
		if err := s.checkFileObject("body."+k, body[k]); err != nil {
			return nil, err
		}
	}

	id := s.newID()
	raw := map[string]any{
		"object":     "page",
		"id":         id,
		"parent":     parent,
		"properties": derive(withPropertyIDs(props)),
		"icon":       body["icon"],
		"cover":      body["cover"],
		"url":        "https://www.notion.so/" + strings.ReplaceAll(id, "-", ""),
		"public_url": nil,
	}
	s.stamp(raw)
	s.pages[id] = &object{raw: raw}
	s.order = append(s.order, id)
	if kind == "page_id" {
		title := ""
		if t, ok := props["title"].(map[string]any); ok {
			if rt, ok := t["title"].([]any); ok {
				title = plain(rt)
			}
		}
		s.appendBlockLocked(pid, map[string]any{"type": "child_page", "child_page": map[string]any{"title": title}}, id)
	}
	return raw, nil
}

// derive fills the fields Notion computes on write: plain_text and href
// of rich text.
func derive(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = derive(val)
		}
		if _, ok := t["annotations"]; ok {
			if _, ok := t["plain_text"]; !ok {
				switch t["type"] {
				case "text":
					txt, _ := t["text"].(map[string]any)
					t["plain_text"], _ = txt["content"].(string)
					t["href"] = nil
					if link, ok := txt["link"].(map[string]any); ok {
						t["href"] = link["url"]
					}
				case "equation":
					eq, _ := t["equation"].(map[string]any)
					t["plain_text"], _ = eq["expression"].(string)
					t["href"] = nil
				default:
					t["plain_text"] = ""
					t["href"] = nil
				}
			}
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = derive(val)
		}
		return t
	}
	return v
}

func withPropertyIDs(props map[string]any) map[string]any {
	out := map[string]any{}
	for name, v := range props {
		pv, _ := v.(map[string]any)
		cp := map[string]any{"id": "prop_" + name}
		for k, val := range pv {
			cp[k] = val
			cp["type"] = k
		}
		out[name] = cp
	}
	return out
}

func plain(rt []any) string {
	var sb strings.Builder
	for _, item := range rt {
		m, _ := item.(map[string]any)
		if t, ok := m["text"].(map[string]any); ok {
			c, _ := t["content"].(string)
			sb.WriteString(c)
		}
	}
	return sb.String()
}

func (s *Server) createDatabase(r *http.Request) (any, error) {
	var body map[string]any
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if err := rejectReadOnly("body", body); err != nil {
		return nil, err
	}
	if err := checkRichText("body", body); err != nil {
		return nil, err
	}
	parent, _ := body["parent"].(map[string]any)
	pid, _ := parent["page_id"].(string)
	if parent["type"] != "page_id" || s.pages[pid] == nil {
		return nil, fail(http.StatusBadRequest, "validation_error", "body.parent must be an existing page_id")
	}
	props, _ := body["properties"].(map[string]any)
	schema := map[string]any{}
	hasTitle := false
	for name, v := range props {
		pv, _ := v.(map[string]any)
		if len(pv) != 1 {
			return nil, fail(http.StatusBadRequest, "validation_error", "body.properties.%s must hold exactly the type key", name)
		}
		for typ, def := range pv {
			switch typ {
			case "rollup", "status", "unique_id", "button", "verification":
				return nil, fail(http.StatusBadRequest, "validation_error", "body.properties.%s: %s cannot be created", name, typ)
			case "relation":
				d, _ := def.(map[string]any)
				target, _ := d["database_id"].(string)
				if s.databases[target] == nil {
					return nil, fail(http.StatusBadRequest, "validation_error", "body.properties.%s.relation.database_id %q not found", name, target)
				}
			case "title":
				hasTitle = true
			}
			schema[name] = map[string]any{"id": "prop_" + name, "name": name, "type": typ, typ: def}
		}
	}
	if !hasTitle {
		return nil, fail(http.StatusBadRequest, "validation_error", "body.properties: a title property is required")
	}
	for _, k := range []string{"icon", "cover"} {
		if err := s.checkFileObject("body."+k, body[k]); err != nil {
			return nil, err
		}
	}

	id := s.newID()
	title, _ := derive(body["title"]).([]any)
	if title == nil {
		title = []any{}
	}
	raw := map[string]any{
		"object":      "database",
		"id":          id,
		"parent":      parent,
		"title":       title,
		"description": body["description"],
		"properties":  schema,
		"is_inline":   body["is_inline"] == true,
		"icon":        body["icon"],
		"cover":       body["cover"],
		"url":         "https://www.notion.so/" + strings.ReplaceAll(id, "-", ""),
		"public_url":  nil,
	}
	if raw["description"] == nil {
		raw["description"] = []any{}
	}
	s.stamp(raw)
	s.databases[id] = &object{raw: raw}
	s.order = append(s.order, id)
	s.appendBlockLocked(pid, map[string]any{"type": "child_database", "child_database": map[string]any{"title": plain(title)}}, id)
	return raw, nil
}

func (s *Server) appendChildren(r *http.Request, parentID string) (any, error) {
	var body struct {
		Children []map[string]any `json:"children"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if len(body.Children) == 0 || len(body.Children) > 100 {
		return nil, fail(http.StatusBadRequest, "validation_error", "body.children must hold 1 to 100 blocks")
	}
	if s.lookup(parentID) == nil || s.databases[parentID] != nil {
		return nil, fail(http.StatusNotFound, "object_not_found", "parent block %s", parentID)
	}
	for i, c := range body.Children {
		if err := s.checkBlock(fmt.Sprintf("body.children[%d]", i), c, 0); err != nil {
			return nil, err
		}
	}
	results := make([]any, 0, len(body.Children))
	for _, c := range body.Children {
		id := s.storeBlock(parentID, c)
		results = append(results, s.blocks[id].raw)
	}
	return map[string]any{"object": "list", "results": results, "has_more": false, "next_cursor": nil}, nil
}

func (s *Server) checkBlock(where string, block map[string]any, depth int) error {
	if err := rejectReadOnly(where, block); err != nil {
		return err
	}
	typ, _ := block["type"].(string)
	payload, ok := block[typ].(map[string]any)
	if typ == "" || !ok {
		return fail(http.StatusBadRequest, "validation_error", "%s: missing block type", where)
	}
	if !blockTypes[typ] {
		return fail(http.StatusBadRequest, "validation_error", "%s.type should be a known block type, instead was %q", where, typ)
	}
	switch typ {
	case "child_page", "child_database", "unsupported", "link_preview", "template":
		return fail(http.StatusBadRequest, "validation_error", "%s: %s blocks cannot be created", where, typ)
	case "image", "video", "audio", "file", "pdf":
		if err := s.checkFileObject(where+"."+typ, payload); err != nil {
			return err
		}
	case "callout":
		if err := s.checkFileObject(where+".callout.icon", payload["icon"]); err != nil {
			return err
		}
	}
	if err := checkRichText(where, payload); err != nil {
		return err
	}
	children, _ := payload["children"].([]any)
	switch typ {
	case "column_list":
		if len(children) < 2 {
			return fail(http.StatusBadRequest, "validation_error", "%s: column_list needs at least two columns", where)
		}
	case "column", "table":
		if len(children) < 1 {
			return fail(http.StatusBadRequest, "validation_error", "%s: %s needs children", where, typ)
		}
	}
	if len(children) > 0 && depth >= 2 {
		return fail(http.StatusBadRequest, "validation_error", "%s: children nested more than two levels deep", where)
	}
	for i, c := range children {
		cm, _ := c.(map[string]any)
		if err := s.checkBlock(fmt.Sprintf("%s.%s.children[%d]", where, typ, i), cm, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) storeBlock(parentID string, block map[string]any) string {
	typ, _ := block["type"].(string)
	payload, _ := block[typ].(map[string]any)
	children, _ := payload["children"].([]any)
	stored := map[string]any{"type": typ, typ: derive(clone(payload))}
	delete(stored[typ].(map[string]any), "children")
	id := s.appendBlockLocked(parentID, stored, "")
	for _, c := range children {
		cm, _ := c.(map[string]any)
		s.storeBlock(id, cm)
	}
	if len(children) > 0 {
		s.blocks[id].raw["has_children"] = true
	}
	return id
}

func (s *Server) createUpload(r *http.Request) (any, error) {
	var body struct {
		Mode     string `json:"mode"`
		Filename string `json:"filename"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if body.Mode != "single_part" || body.Filename == "" {
		return nil, fail(http.StatusBadRequest, "validation_error", "single_part uploads need a filename")
	}
	id := s.newID()
	s.uploads[id] = nil
	return map[string]any{"object": "file_upload", "id": id, "status": "pending", "filename": body.Filename}, nil
}

func (s *Server) sendUpload(r *http.Request, id string) (any, error) {
	if _, ok := s.uploads[id]; !ok {
		return nil, fail(http.StatusNotFound, "object_not_found", "upload %s", id)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, fail(http.StatusBadRequest, "validation_error", "multipart: %v", err)
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		return nil, fail(http.StatusBadRequest, "validation_error", "missing file part")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	s.uploads[id] = data
	return map[string]any{"object": "file_upload", "id": id, "status": "uploaded"}, nil
}
