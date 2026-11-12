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

package fakenotion

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PlakarKorp/integrations/notion/api"
)

// New starts a fake Notion.
func New() *Server {
	s := newServer()
	s.srv = httptest.NewServer(s)
	s.base = s.srv.URL
	return s
}

func (s *Server) URL() string { return s.base }
func (s *Server) Close()      { s.srv.Close() }

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func bad(format string, args ...any) *apiError {
	return &apiError{status: 400, code: "validation_error", message: fmt.Sprintf(format, args...)}
}

func notFound(id string) *apiError {
	return &apiError{status: 404, code: "object_not_found", message: "Could not find object " + id}
}

type route struct {
	method string
	re     *regexp.Regexp
	fn     func(s *Server, r *http.Request, m []string, body map[string]any) (any, error)
}

var routes = []route{
	{"GET", regexp.MustCompile(`^/v1/users/me$`), (*Server).getMe},
	{"GET", regexp.MustCompile(`^/v1/users$`), (*Server).listUsers},
	{"GET", regexp.MustCompile(`^/v1/users/([^/]+)$`), (*Server).getUser},
	{"POST", regexp.MustCompile(`^/v1/search$`), (*Server).search},
	{"GET", regexp.MustCompile(`^/v1/pages/([^/]+)$`), (*Server).getPage},
	{"GET", regexp.MustCompile(`^/v1/pages/([^/]+)/markdown$`), (*Server).getMarkdown},
	{"GET", regexp.MustCompile(`^/v1/pages/([^/]+)/properties/([^/]+)$`), (*Server).getProperty},
	{"POST", regexp.MustCompile(`^/v1/pages$`), (*Server).createPage},
	{"PATCH", regexp.MustCompile(`^/v1/pages/([^/]+)$`), (*Server).updatePage},
	{"GET", regexp.MustCompile(`^/v1/databases/([^/]+)$`), (*Server).getDatabase},
	{"POST", regexp.MustCompile(`^/v1/databases$`), (*Server).createDatabase},
	{"PATCH", regexp.MustCompile(`^/v1/databases/([^/]+)$`), (*Server).updateDatabase},
	{"GET", regexp.MustCompile(`^/v1/data_sources/([^/]+)$`), (*Server).getDataSource},
	{"POST", regexp.MustCompile(`^/v1/data_sources$`), (*Server).createDataSource},
	{"PATCH", regexp.MustCompile(`^/v1/data_sources/([^/]+)$`), (*Server).updateDataSource},
	{"POST", regexp.MustCompile(`^/v1/data_sources/([^/]+)/query$`), (*Server).queryDataSource},
	{"GET", regexp.MustCompile(`^/v1/blocks/([^/]+)$`), (*Server).getBlock},
	{"GET", regexp.MustCompile(`^/v1/blocks/([^/]+)/children$`), (*Server).getChildren},
	{"PATCH", regexp.MustCompile(`^/v1/blocks/([^/]+)/children$`), (*Server).appendChildren},
	{"DELETE", regexp.MustCompile(`^/v1/blocks/([^/]+)$`), (*Server).deleteBlock},
	{"GET", regexp.MustCompile(`^/v1/comments$`), (*Server).listComments},
	{"POST", regexp.MustCompile(`^/v1/comments$`), (*Server).createComment},
	{"GET", regexp.MustCompile(`^/v1/views$`), (*Server).listViews},
	{"GET", regexp.MustCompile(`^/v1/views/([^/]+)$`), (*Server).getView},
	{"POST", regexp.MustCompile(`^/v1/views$`), (*Server).createView},
	{"POST", regexp.MustCompile(`^/v1/file_uploads$`), (*Server).createUpload},
	{"POST", regexp.MustCompile(`^/v1/file_uploads/([^/]+)/send$`), (*Server).sendUpload},
	{"POST", regexp.MustCompile(`^/v1/file_uploads/([^/]+)/complete$`), (*Server).completeUpload},
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/files/") {
		s.serveFile(w, r)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		writeErr(w, &apiError{401, "unauthorized", "API token is invalid."})
		return
	}
	if v := r.Header.Get("Notion-Version"); v != api.Version {
		writeErr(w, &apiError{400, "invalid_request", "unsupported Notion-Version " + v})
		return
	}

	for _, rt := range routes {
		if rt.method != r.Method {
			continue
		}
		m := rt.re.FindStringSubmatch(r.URL.Path)
		if m == nil {
			continue
		}
		pattern := rt.re.String()
		s.Requests[r.Method+" "+pattern]++
		s.calls++
		if s.FailEvery > 0 && s.calls%s.FailEvery == 0 {
			w.Header().Set("Retry-After", "1")
			writeErr(w, &apiError{429, "rate_limited", "slow down"})
			return
		}

		var body map[string]any
		if r.Method != http.MethodGet && !strings.HasSuffix(r.URL.Path, "/send") {
			raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			if err != nil {
				writeErr(w, bad("reading body: %v", err))
				return
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &body); err != nil {
					writeErr(w, &apiError{400, "invalid_json", err.Error()})
					return
				}
			}
		}
		for i := range m[1:] {
			if api.IsID(m[i+1]) {
				m[i+1] = api.ExpandID(m[i+1])
			}
		}
		out, err := rt.fn(s, r, m, body)
		if err != nil {
			var e *apiError
			var ae *apiError
			if errors.As(err, &ae) {
				e = ae
			} else {
				e = &apiError{500, "internal_server_error", err.Error()}
			}
			writeErr(w, e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	writeErr(w, &apiError{404, "object_not_found", "no such route " + r.Method + " " + r.URL.Path})
}

func writeErr(w http.ResponseWriter, e *apiError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "error", "status": e.status, "code": e.code, "message": e.message,
	})
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, _ := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if exp == 0 || s.now().Unix() > exp {
		http.Error(w, "Request has expired", http.StatusForbidden)
		return
	}
	id := s.fileID(r.URL.String())
	f, ok := s.files[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", f.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(f.data)))
	_, _ = w.Write(f.data)
}

// paginate answers a list request from items with a numeric cursor.
func paginate(r *http.Request, body map[string]any, items []any, typ string) map[string]any {
	size := 100
	cursor := 0
	if r.Method == http.MethodGet {
		if v := r.URL.Query().Get("page_size"); v != "" {
			size, _ = strconv.Atoi(v)
		}
		if v := r.URL.Query().Get("start_cursor"); v != "" {
			cursor, _ = strconv.Atoi(v)
		}
	} else {
		if v, ok := body["page_size"].(float64); ok {
			size = int(v)
		}
		if v, ok := body["start_cursor"].(string); ok {
			cursor, _ = strconv.Atoi(v)
		}
	}
	if size <= 0 || size > 100 {
		size = 100
	}
	if cursor > len(items) {
		cursor = len(items)
	}
	end := cursor + size
	if end > len(items) {
		end = len(items)
	}
	out := map[string]any{
		"object":         "list",
		"type":           typ,
		typ:              map[string]any{},
		"results":        items[cursor:end],
		"has_more":       end < len(items),
		"next_cursor":    nil,
		"request_status": map[string]any{"type": "complete"},
	}
	if end < len(items) {
		out["next_cursor"] = strconv.Itoa(end)
	}
	return out
}

// --- users ---

func (s *Server) getMe(r *http.Request, m []string, body map[string]any) (any, error) {
	return s.bot, nil
}

func (s *Server) listUsers(r *http.Request, m []string, body map[string]any) (any, error) {
	var items []any
	for _, id := range s.userList {
		items = append(items, s.users[id])
	}
	items = append(items, s.bot)
	return paginate(r, body, items, "user"), nil
}

func (s *Server) getUser(r *http.Request, m []string, body map[string]any) (any, error) {
	if u, ok := s.users[m[1]]; ok {
		return u, nil
	}
	if m[1] == s.actor() {
		return s.bot, nil
	}
	return nil, notFound(m[1])
}

// --- search ---

func (s *Server) search(r *http.Request, m []string, body map[string]any) (any, error) {
	want := "page"
	if f, ok := body["filter"].(map[string]any); ok {
		if v, ok := f["value"].(string); ok {
			want = v
		}
	}
	if want == "database" {
		return nil, bad("filter value must be page or data_source")
	}
	var items []*object
	for id, o := range s.objects {
		if strings.HasPrefix(id, "block:") || o.kind != want {
			continue
		}
		if t, _ := o.raw["in_trash"].(bool); t {
			continue
		}
		items = append(items, o)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].raw["last_edited_time"].(string), items[j].raw["last_edited_time"].(string)
		if a != b {
			return a > b
		}
		return items[i].raw["id"].(string) < items[j].raw["id"].(string)
	})
	var out []any
	for _, o := range items {
		out = append(out, s.render(o.raw))
	}
	return paginate(r, body, out, "page_or_data_source"), nil
}

// --- pages ---

func (s *Server) getPage(r *http.Request, m []string, body map[string]any) (any, error) {
	o := s.objects[m[1]]
	if o == nil || o.kind != "page" {
		return nil, notFound(m[1])
	}
	return s.render(o.raw), nil
}

func (s *Server) getProperty(r *http.Request, m []string, body map[string]any) (any, error) {
	o := s.objects[m[1]]
	if o == nil || o.kind != "page" {
		return nil, notFound(m[1])
	}
	props := o.raw["properties"].(map[string]any)
	var prop map[string]any
	for name, p := range props {
		pm := p.(map[string]any)
		if name == m[2] || pm["id"] == m[2] {
			prop = pm
		}
	}
	if prop == nil {
		return nil, notFound(m[2])
	}
	typ := prop["type"].(string)
	switch typ {
	case "title", "rich_text", "people", "relation":
		var items []any
		arr, _ := prop[typ].([]any)
		for _, it := range arr {
			items = append(items, map[string]any{"object": "property_item", "id": prop["id"], "type": typ, typ: it})
		}
		out := paginate(r, body, items, "property_item")
		out["property_item"] = map[string]any{"id": prop["id"], "type": typ, "next_url": nil}
		return out, nil
	}
	return map[string]any{"object": "property_item", "id": prop["id"], "type": typ, typ: prop[typ]}, nil
}

var readOnlyProps = map[string]bool{
	"formula": true, "rollup": true, "created_time": true, "created_by": true,
	"last_edited_time": true, "last_edited_by": true, "unique_id": true, "button": true,
}

func (s *Server) createPage(r *http.Request, m []string, body map[string]any) (any, error) {
	parent, _ := body["parent"].(map[string]any)
	if parent == nil {
		return nil, bad("parent is required")
	}
	pkind, _ := parent["type"].(string)
	if pkind == "" {
		for _, k := range []string{"page_id", "data_source_id", "database_id"} {
			if _, ok := parent[k]; ok {
				pkind = k
			}
		}
	}
	pid, _ := parent[pkind].(string)
	pid = api.ExpandID(pid)
	switch pkind {
	case "page_id", "data_source_id", "database_id":
		po := s.objects[pid]
		if po == nil || po.kind != strings.TrimSuffix(pkind, "_id") {
			return nil, notFound(pid)
		}
	case "workspace":
		return nil, bad("internal connections cannot create workspace-level pages")
	default:
		return nil, bad("unsupported parent type %q", pkind)
	}

	props, _ := body["properties"].(map[string]any)
	title := ""
	rowProps := map[string]any{}
	if pkind == "data_source_id" {
		schema := s.objects[pid].raw["properties"].(map[string]any)
		for name, v := range props {
			sv, ok := schema[name].(map[string]any)
			if !ok {
				return nil, bad("%s is not a property that exists", name)
			}
			typ := sv["type"].(string)
			if readOnlyProps[typ] {
				return nil, bad("%s is a read-only property of type %s", name, typ)
			}
			val, err := s.propertyValue(typ, v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if typ == "title" {
				title = plain(val)
			}
			rowProps[name] = map[string]any{typ: val}
		}
	} else {
		for name, v := range props {
			if name != "title" && name != "Name" {
				return nil, bad("only title is allowed for pages parented by a page")
			}
			val, err := s.propertyValue("title", v)
			if err != nil {
				return nil, err
			}
			title = plain(val)
		}
	}

	icon, err := s.iconValue(body["icon"], false)
	if err != nil {
		return nil, err
	}
	cover, err := s.coverValue(body["cover"])
	if err != nil {
		return nil, err
	}

	children, _ := body["children"].([]any)
	if len(children) > 100 {
		return nil, bad("children must have at most 100 items")
	}
	for _, c := range children {
		if err := s.validateBlock(c, 0); err != nil {
			return nil, err
		}
	}

	after := ""
	if pos, ok := body["position"].(map[string]any); ok {
		switch pos["type"] {
		case "page_start":
			after = "start"
		case "page_end", nil:
		case "after_block":
			ab, _ := pos["after_block"].(map[string]any)
			id := api.ExpandID(fmt.Sprint(ab["id"]))
			if s.lookup(id) == nil {
				return nil, notFound(id)
			}
			after = id
		default:
			return nil, bad("bad position type")
		}
	}

	id := s.addPage(pkind, pid, title, rowProps, after)
	o := s.objects[id]
	o.raw["icon"] = icon
	o.raw["cover"] = cover
	for _, c := range children {
		s.materialize(id, c.(map[string]any), "")
	}
	return s.render(o.raw), nil
}

func (s *Server) updatePage(r *http.Request, m []string, body map[string]any) (any, error) {
	o := s.objects[m[1]]
	if o == nil || o.kind != "page" {
		return nil, notFound(m[1])
	}
	if v, ok := body["icon"]; ok {
		icon, err := s.iconValue(v, false)
		if err != nil {
			return nil, err
		}
		o.raw["icon"] = icon
	}
	if v, ok := body["cover"]; ok {
		cover, err := s.coverValue(v)
		if err != nil {
			return nil, err
		}
		o.raw["cover"] = cover
	}
	if v, ok := body["in_trash"].(bool); ok {
		o.raw["in_trash"] = v
		o.raw["archived"] = v
	}
	if props, ok := body["properties"].(map[string]any); ok {
		parent := o.raw["parent"].(map[string]any)
		cur := o.raw["properties"].(map[string]any)
		if parent["type"] == "data_source_id" {
			schema := s.objects[o.parentID].raw["properties"].(map[string]any)
			for name, v := range props {
				sv, ok := schema[name].(map[string]any)
				if !ok {
					return nil, bad("%s is not a property that exists", name)
				}
				typ := sv["type"].(string)
				if readOnlyProps[typ] {
					return nil, bad("%s is read-only", name)
				}
				val, err := s.propertyValue(typ, v)
				if err != nil {
					return nil, err
				}
				cur[name] = map[string]any{"id": sv["id"], "type": typ, typ: val}
			}
		} else {
			for name, v := range props {
				if name != "title" {
					return nil, bad("only title can be set")
				}
				val, err := s.propertyValue("title", v)
				if err != nil {
					return nil, err
				}
				cur["title"] = map[string]any{"id": "title", "type": "title", "title": val}
				if b := s.block(m[1]); b != nil {
					b.raw["child_page"] = map[string]any{"title": plain(val)}
				}
			}
		}
	}
	o.raw["last_edited_time"] = s.stamp()
	return s.render(o.raw), nil
}

// propertyValue checks a property value as sent by a client and
// returns what gets stored.  v is {<type>: value} or {"type": t, t: value}.
func (s *Server) propertyValue(typ string, v any) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, bad("property value must be an object")
	}
	for k := range m {
		if k != typ && k != "type" && k != "id" {
			return nil, bad("unexpected key %q in a %s value", k, typ)
		}
	}
	val, ok := m[typ]
	if !ok {
		return nil, bad("missing %s in value", typ)
	}
	switch typ {
	case "title", "rich_text":
		if err := validateRichText(val); err != nil {
			return nil, err
		}
		return s.storeRichText(val), nil
	case "select", "status":
		if val == nil {
			return nil, nil //nolint:nilnil // a cleared value is stored as JSON null
		}
		om, _ := val.(map[string]any)
		name, _ := om["name"].(string)
		if name == "" {
			return nil, bad("%s needs a name", typ)
		}
		return map[string]any{"id": "opt-" + name, "name": name, "color": om["color"]}, nil
	case "multi_select":
		arr, _ := val.([]any)
		var out []any
		for _, it := range arr {
			om, _ := it.(map[string]any)
			name, _ := om["name"].(string)
			if name == "" {
				return nil, bad("multi_select option needs a name")
			}
			out = append(out, map[string]any{"id": "opt-" + name, "name": name, "color": om["color"]})
		}
		if out == nil {
			out = []any{}
		}
		return out, nil
	case "people":
		arr, _ := val.([]any)
		var out []any
		for _, it := range arr {
			um, _ := it.(map[string]any)
			id := api.ExpandID(fmt.Sprint(um["id"]))
			if _, ok := s.users[id]; !ok && id != s.actor() {
				return nil, bad("user %s does not exist", id)
			}
			out = append(out, map[string]any{"object": "user", "id": id})
		}
		if out == nil {
			out = []any{}
		}
		return out, nil
	case "relation":
		arr, _ := val.([]any)
		var out []any
		for _, it := range arr {
			rm, _ := it.(map[string]any)
			id := api.ExpandID(fmt.Sprint(rm["id"]))
			if o := s.objects[id]; o == nil || o.kind != "page" {
				return nil, bad("related page %s does not exist", id)
			}
			out = append(out, map[string]any{"id": id})
		}
		if out == nil {
			out = []any{}
		}
		return out, nil
	case "files":
		arr, _ := val.([]any)
		var out []any
		for _, it := range arr {
			fm, _ := it.(map[string]any)
			name, _ := fm["name"].(string)
			stored, err := s.fileValue(fm)
			if err != nil {
				return nil, err
			}
			stored["name"] = name
			out = append(out, stored)
		}
		if out == nil {
			out = []any{}
		}
		return out, nil
	case "date":
		if val == nil {
			return nil, nil //nolint:nilnil // a cleared value is stored as JSON null
		}
		dm, _ := val.(map[string]any)
		if _, ok := dm["start"].(string); !ok {
			return nil, bad("date needs a start")
		}
		return map[string]any{"start": dm["start"], "end": dm["end"], "time_zone": dm["time_zone"]}, nil
	case "number", "checkbox", "url", "email", "phone_number", "place", "verification":
		return val, nil
	}
	return nil, bad("unsupported property type %s", typ)
}

// fileValue accepts an external or file_upload reference and returns
// the stored form.
func (s *Server) fileValue(m map[string]any) (map[string]any, error) {
	switch m["type"] {
	case "external":
		ext, _ := m["external"].(map[string]any)
		if _, ok := ext["url"].(string); !ok {
			return nil, bad("external needs a url")
		}
		return map[string]any{"type": "external", "external": map[string]any{"url": ext["url"]}}, nil
	case "file_upload":
		fu, _ := m["file_upload"].(map[string]any)
		id := api.ExpandID(fmt.Sprint(fu["id"]))
		up := s.uploads[id]
		if up == nil || up.status != "uploaded" {
			return nil, bad("file upload %s is not uploaded", id)
		}
		var data []byte
		for i := 1; i <= up.nparts; i++ {
			data = append(data, up.parts[i]...)
		}
		if up.nparts == 0 {
			data = up.parts[0]
		}
		fid := s.newID()
		s.files[fid] = &file{name: up.filename, contentType: up.contentType, data: data}
		return map[string]any{"type": "file", "file": map[string]any{"url": "notionfile://" + fid + "/" + up.filename}}, nil
	case "file":
		return nil, bad("file objects cannot be written, use file_upload or external")
	}
	return nil, bad("bad file reference")
}

func (s *Server) iconValue(v any, callout bool) (any, error) {
	if v == nil {
		return nil, nil //nolint:nilnil // no icon is stored as JSON null
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, bad("icon must be an object")
	}
	switch m["type"] {
	case "emoji":
		return map[string]any{"type": "emoji", "emoji": m["emoji"]}, nil
	case "custom_emoji", "icon":
		return m, nil
	case "external", "file_upload":
		if callout && m["type"] == "file_upload" {
			return nil, bad("callout icons cannot be file uploads")
		}
		return s.fileValue(m)
	}
	return nil, bad("bad icon type %v", m["type"])
}

func (s *Server) coverValue(v any) (any, error) {
	if v == nil {
		return nil, nil //nolint:nilnil // no cover is stored as JSON null
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, bad("cover must be an object")
	}
	switch m["type"] {
	case "external", "file_upload":
		return s.fileValue(m)
	}
	return nil, bad("bad cover type %v", m["type"])
}

func validateRichText(v any) error {
	arr, ok := v.([]any)
	if !ok {
		return bad("rich text must be an array")
	}
	if len(arr) > 100 {
		return bad("rich text arrays are limited to 100 items")
	}
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			return bad("rich text item must be an object")
		}
		if _, ok := m["plain_text"]; ok {
			return bad("plain_text is read-only")
		}
		if _, ok := m["href"]; ok {
			return bad("href is read-only")
		}
		typ, _ := m["type"].(string)
		if typ == "" {
			for _, k := range []string{"text", "mention", "equation"} {
				if _, ok := m[k]; ok {
					typ = k
				}
			}
		}
		switch typ {
		case "text":
			t, _ := m["text"].(map[string]any)
			c, _ := t["content"].(string)
			if len(c) > 2000 {
				return bad("text.content is limited to 2000 characters")
			}
		case "mention", "equation":
		default:
			return bad("bad rich text type %q", typ)
		}
	}
	return nil
}

// storeRichText completes what a client sent with the read-only bits.
func (s *Server) storeRichText(v any) any {
	arr, _ := v.([]any)
	out := []any{}
	for _, it := range arr {
		m := it.(map[string]any)
		cp := map[string]any{}
		for k, vv := range m {
			cp[k] = vv
		}
		typ, _ := cp["type"].(string)
		if typ == "" {
			for _, k := range []string{"text", "mention", "equation"} {
				if _, ok := cp[k]; ok {
					typ = k
					cp["type"] = k
				}
			}
		}
		if _, ok := cp["annotations"]; !ok {
			cp["annotations"] = map[string]any{"bold": false, "italic": false, "strikethrough": false, "underline": false, "code": false, "color": "default"}
		}
		switch typ {
		case "text":
			t := cp["text"].(map[string]any)
			cp["plain_text"] = t["content"]
			cp["href"] = nil
			if l, ok := t["link"].(map[string]any); ok {
				cp["href"] = l["url"]
			}
		case "mention":
			cp["plain_text"] = "@mention"
			cp["href"] = nil
		case "equation":
			cp["plain_text"] = cp["equation"].(map[string]any)["expression"]
			cp["href"] = nil
		}
		out = append(out, cp)
	}
	return out
}

// --- databases and data sources ---

func (s *Server) getDatabase(r *http.Request, m []string, body map[string]any) (any, error) {
	o := s.objects[m[1]]
	if o == nil || o.kind != "database" {
		return nil, notFound(m[1])
	}
	return s.render(o.raw), nil
}

func (s *Server) createDatabase(r *http.Request, m []string, body map[string]any) (any, error) {
	parent, _ := body["parent"].(map[string]any)
	pid := api.ExpandID(fmt.Sprint(parent["page_id"]))
	if parent["type"] != "page_id" || s.objects[pid] == nil || s.objects[pid].kind != "page" {
		return nil, bad("parent must be an existing page")
	}
	title := ""
	if t, ok := body["title"]; ok {
		if err := validateRichText(t); err != nil {
			return nil, err
		}
		title = plain(t)
	}
	schema := map[string]any{}
	if ids, ok := body["initial_data_source"].(map[string]any); ok {
		props, _ := ids["properties"].(map[string]any)
		var err error
		schema, err = s.validateSchema(props, "", true)
		if err != nil {
			return nil, err
		}
	}
	inline, _ := body["is_inline"].(bool)
	id, dsID := s.addDatabase("page_id", pid, title, schema, inline)
	o := s.objects[id]
	if d, ok := body["description"]; ok {
		if err := validateRichText(d); err != nil {
			return nil, err
		}
		o.raw["description"] = s.storeRichText(d)
	}
	icon, err := s.iconValue(body["icon"], false)
	if err != nil {
		return nil, err
	}
	o.raw["icon"] = icon
	cover, err := s.coverValue(body["cover"])
	if err != nil {
		return nil, err
	}
	o.raw["cover"] = cover
	if v, ok := body["show_data_source_title"].(bool); ok {
		o.raw["show_data_source_title"] = v
	}
	_ = dsID
	return s.render(o.raw), nil
}

func (s *Server) updateDatabase(r *http.Request, m []string, body map[string]any) (any, error) {
	o := s.objects[m[1]]
	if o == nil || o.kind != "database" {
		return nil, notFound(m[1])
	}
	if t, ok := body["title"]; ok {
		if err := validateRichText(t); err != nil {
			return nil, err
		}
		o.raw["title"] = s.storeRichText(t)
	}
	if v, ok := body["icon"]; ok {
		icon, err := s.iconValue(v, false)
		if err != nil {
			return nil, err
		}
		o.raw["icon"] = icon
	}
	if v, ok := body["cover"]; ok {
		cover, err := s.coverValue(v)
		if err != nil {
			return nil, err
		}
		o.raw["cover"] = cover
	}
	if v, ok := body["in_trash"].(bool); ok {
		o.raw["in_trash"] = v
		o.raw["archived"] = v
	}
	if v, ok := body["is_inline"].(bool); ok {
		o.raw["is_inline"] = v
	}
	o.raw["last_edited_time"] = s.stamp()
	return s.render(o.raw), nil
}

func (s *Server) getDataSource(r *http.Request, m []string, body map[string]any) (any, error) {
	o := s.objects[m[1]]
	if o == nil || o.kind != "data_source" {
		return nil, notFound(m[1])
	}
	return s.render(o.raw), nil
}

func (s *Server) createDataSource(r *http.Request, m []string, body map[string]any) (any, error) {
	parent, _ := body["parent"].(map[string]any)
	dbID := api.ExpandID(fmt.Sprint(parent["database_id"]))
	if parent["type"] != "database_id" || s.objects[dbID] == nil || s.objects[dbID].kind != "database" {
		return nil, bad("parent must be an existing database")
	}
	props, _ := body["properties"].(map[string]any)
	schema, err := s.validateSchema(props, "", true)
	if err != nil {
		return nil, err
	}
	title := ""
	if t, ok := body["title"]; ok {
		if err := validateRichText(t); err != nil {
			return nil, err
		}
		title = plain(t)
	}
	id := s.addDataSource(dbID, title, schema)
	o := s.objects[id]
	icon, err := s.iconValue(body["icon"], false)
	if err != nil {
		return nil, err
	}
	o.raw["icon"] = icon
	s.wireDualRelations(id)
	return s.render(o.raw), nil
}

func (s *Server) updateDataSource(r *http.Request, m []string, body map[string]any) (any, error) {
	o := s.objects[m[1]]
	if o == nil || o.kind != "data_source" {
		return nil, notFound(m[1])
	}
	if t, ok := body["title"]; ok {
		if err := validateRichText(t); err != nil {
			return nil, err
		}
		o.raw["title"] = s.storeRichText(t)
	}
	if props, ok := body["properties"].(map[string]any); ok {
		cur := o.raw["properties"].(map[string]any)
		adds := map[string]any{}
		for name, v := range props {
			if v == nil {
				if cur[name].(map[string]any)["type"] == "title" {
					return nil, bad("the title property cannot be removed")
				}
				delete(cur, name)
				continue
			}
			if vm, ok := v.(map[string]any); ok && len(vm) == 1 && vm["name"] != nil {
				// a rename
				old, ok := cur[name].(map[string]any)
				if !ok {
					return nil, bad("%s is not a property that exists", name)
				}
				newName, _ := vm["name"].(string)
				if newName == "" {
					return nil, bad("%s: empty name", name)
				}
				if _, taken := cur[newName]; taken && newName != name {
					return nil, bad("%s: a property named %q exists", name, newName)
				}
				delete(cur, name)
				old["name"] = newName
				cur[newName] = old
				continue
			}
			adds[name] = v
		}
		schema, err := s.validateSchema(adds, m[1], false)
		if err != nil {
			return nil, err
		}
		for name, v := range schema {
			nm := v.(map[string]any)
			if old, ok := cur[name].(map[string]any); ok {
				nm["id"] = old["id"]
			} else {
				nm["id"] = "prop" + strconv.Itoa(s.seq) + name
			}
			nm["name"] = name
			cur[name] = nm
		}
		s.wireDualRelations(m[1])
	}
	o.raw["last_edited_time"] = s.stamp()
	return s.render(o.raw), nil
}

// validateSchema checks property configurations as sent by a client.
// self is the data source being updated, for rollups over its own
// relations; needTitle requires exactly one title property.
func (s *Server) validateSchema(props map[string]any, self string, needTitle bool) (map[string]any, error) {
	out := map[string]any{}
	titles := 0
	for name, v := range props {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, bad("%s: property configuration must be an object", name)
		}
		for _, k := range []string{"id", "name"} {
			if _, ok := m[k]; ok {
				return nil, bad("%s: %s is read-only", name, k)
			}
		}
		typ, _ := m["type"].(string)
		if typ == "" {
			for k := range m {
				if k != "description" {
					typ = k
				}
			}
		}
		cfg, _ := m[typ].(map[string]any)
		if cfg == nil {
			return nil, bad("%s: missing %s configuration", name, typ)
		}
		switch typ {
		case "title":
			titles++
		case "rich_text", "number", "select", "multi_select", "status", "date", "people", "files",
			"checkbox", "url", "email", "phone_number", "created_time", "created_by",
			"last_edited_time", "last_edited_by", "unique_id", "place", "formula":
			if typ == "formula" {
				if _, ok := cfg["expression"].(string); !ok {
					return nil, bad("%s: formula needs an expression", name)
				}
			}
			if typ == "select" || typ == "multi_select" || typ == "status" {
				opts, _ := cfg["options"].([]any)
				for _, o := range opts {
					om, _ := o.(map[string]any)
					if _, ok := om["id"]; ok {
						return nil, bad("%s: option ids are read-only", name)
					}
					if _, ok := om["name"].(string); !ok {
						return nil, bad("%s: option needs a name", name)
					}
				}
			}
		case "relation":
			if _, ok := cfg["database_id"]; ok {
				return nil, bad("%s: relation must use data_source_id", name)
			}
			target := api.ExpandID(fmt.Sprint(cfg["data_source_id"]))
			if t := s.objects[target]; t == nil || t.kind != "data_source" {
				return nil, bad("%s: relation target %s does not exist", name, target)
			}
			switch cfg["type"] {
			case "single_property", "dual_property":
			default:
				return nil, bad("%s: relation type must be single_property or dual_property", name)
			}
			cfg["data_source_id"] = target
		case "rollup":
			rel, _ := cfg["relation_property_name"].(string)
			if rel == "" {
				return nil, bad("%s: rollup needs relation_property_name", name)
			}
			var relCfg map[string]any
			if p, ok := props[rel].(map[string]any); ok {
				relCfg, _ = p["relation"].(map[string]any)
			} else if self != "" {
				if p, ok := s.objects[self].raw["properties"].(map[string]any)[rel].(map[string]any); ok {
					relCfg, _ = p["relation"].(map[string]any)
				}
			}
			if relCfg == nil {
				return nil, bad("%s: %s is not a relation", name, rel)
			}
			tgt := s.objects[api.ExpandID(fmt.Sprint(relCfg["data_source_id"]))]
			rp, _ := cfg["rollup_property_name"].(string)
			if tgt == nil || tgt.raw["properties"].(map[string]any)[rp] == nil {
				return nil, bad("%s: rollup property %q not found in target", name, rp)
			}
			if _, ok := cfg["function"].(string); !ok {
				return nil, bad("%s: rollup needs a function", name)
			}
		case "button", "verification":
			return nil, bad("%s: %s cannot be created through the API", name, typ)
		default:
			return nil, bad("%s: unknown property type %q", name, typ)
		}
		out[name] = map[string]any{"type": typ, typ: cfg}
		if d, ok := m["description"]; ok {
			out[name].(map[string]any)["description"] = d
		}
	}
	if needTitle && titles != 1 {
		return nil, bad("a data source needs exactly one title property")
	}
	if !needTitle && titles > 1 {
		return nil, bad("only one title property is allowed")
	}
	return out, nil
}

// wireDualRelations creates the synced property on the other side of
// dual relations, as Notion does.
func (s *Server) wireDualRelations(dsID string) {
	ds := s.objects[dsID]
	for name, v := range ds.raw["properties"].(map[string]any) {
		pm := v.(map[string]any)
		if pm["type"] != "relation" {
			continue
		}
		cfg := pm["relation"].(map[string]any)
		if cfg["type"] != "dual_property" {
			continue
		}
		dual, _ := cfg["dual_property"].(map[string]any)
		if dual == nil {
			dual = map[string]any{}
			cfg["dual_property"] = dual
		}
		if _, ok := dual["synced_property_id"]; ok {
			continue
		}
		target := s.objects[cfg["data_source_id"].(string)]
		syncedName, _ := dual["synced_property_name"].(string)
		if syncedName == "" {
			syncedName = "Related to " + plain(ds.raw["title"]) + " (" + name + ")"
		}
		tprops := target.raw["properties"].(map[string]any)
		if existing, ok := tprops[syncedName].(map[string]any); ok && existing["type"] == "relation" {
			dual["synced_property_id"] = existing["id"]
			dual["synced_property_name"] = syncedName
			continue
		}
		syncedID := "prop" + strconv.Itoa(s.seq) + syncedName
		tprops[syncedName] = map[string]any{
			"id": syncedID, "name": syncedName, "type": "relation",
			"relation": map[string]any{
				"data_source_id": dsID, "database_id": ds.parentID, "type": "dual_property",
				"dual_property": map[string]any{"synced_property_name": name, "synced_property_id": pm["id"]},
			},
		}
		dual["synced_property_id"] = syncedID
		dual["synced_property_name"] = syncedName
	}
}

func (s *Server) queryDataSource(r *http.Request, m []string, body map[string]any) (any, error) {
	ds := s.objects[m[1]]
	if ds == nil || ds.kind != "data_source" {
		return nil, notFound(m[1])
	}
	after := ""
	if f, ok := body["filter"].(map[string]any); ok {
		if f["timestamp"] == "created_time" {
			ct, _ := f["created_time"].(map[string]any)
			after, _ = ct["on_or_after"].(string)
		}
	}
	var rows []*object
	for _, id := range ds.children {
		o := s.objects[id]
		if t, _ := o.raw["in_trash"].(bool); t {
			continue
		}
		if after != "" && o.raw["created_time"].(string) < after {
			continue
		}
		rows = append(rows, o)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].raw["created_time"].(string) < rows[j].raw["created_time"].(string)
	})
	var items []any
	for _, o := range rows {
		items = append(items, s.render(o.raw))
	}
	incomplete := false
	if s.QueryLimit > 0 && len(items) > s.QueryLimit {
		items = items[:s.QueryLimit]
		incomplete = true
	}
	out := paginate(r, body, items, "page_or_data_source")
	if incomplete && !out["has_more"].(bool) {
		out["request_status"] = map[string]any{"type": "incomplete", "incomplete_reason": "query_result_limit_reached"}
	}
	return out, nil
}

// --- blocks ---

func (s *Server) getBlock(r *http.Request, m []string, body map[string]any) (any, error) {
	if b := s.block(m[1]); b != nil {
		return s.render(b.raw), nil
	}
	return nil, notFound(m[1])
}

func (s *Server) getChildren(r *http.Request, m []string, body map[string]any) (any, error) {
	o := s.lookup(m[1])
	if o == nil || (o.kind != "page" && o.kind != "block") {
		return nil, notFound(m[1])
	}
	var items []any
	for _, c := range o.children {
		items = append(items, s.render(s.block(c).raw))
	}
	return paginate(r, body, items, "block"), nil
}

var notAppendable = map[string]bool{
	"child_page": true, "child_database": true, "link_preview": true, "unsupported": true,
	"template": true, "transcription": true, "meeting_notes": true,
}

var mediaTypes = map[string]bool{"file": true, "image": true, "video": true, "pdf": true, "audio": true}

var readOnlyBlockKeys = []string{"id", "created_time", "created_by", "last_edited_time", "last_edited_by", "has_children", "in_trash", "archived", "parent"}

func (s *Server) validateBlock(v any, depth int) error {
	b, ok := v.(map[string]any)
	if !ok {
		return bad("block must be an object")
	}
	for _, k := range readOnlyBlockKeys {
		if _, ok := b[k]; ok {
			return bad("%s is read-only on blocks", k)
		}
	}
	typ, _ := b["type"].(string)
	if typ == "" {
		for k := range b {
			if k != "object" {
				typ = k
			}
		}
	}
	if notAppendable[typ] {
		return bad("blocks of type %s cannot be appended", typ)
	}
	payload, ok := b[typ].(map[string]any)
	if !ok {
		return bad("missing %s payload", typ)
	}
	for k, v := range payload {
		// an original synced block is the one null the API takes
		if v == nil && !(typ == "synced_block" && k == "synced_from") {
			return bad("body.children[0].%s.%s should be an object or `undefined`, instead was `null`", typ, k)
		}
	}
	for _, k := range []string{"rich_text", "caption"} {
		if rt, ok := payload[k]; ok {
			if err := validateRichText(rt); err != nil {
				return err
			}
		}
	}
	if typ == "table_row" {
		cells, _ := payload["cells"].([]any)
		for _, c := range cells {
			if err := validateRichText(c); err != nil {
				return err
			}
		}
	}
	if mediaTypes[typ] {
		switch payload["type"] {
		case "external", "file_upload":
			if _, err := s.fileValue(payload); err != nil {
				return err
			}
		default:
			return bad("%s block needs an external or file_upload source", typ)
		}
	}
	if typ == "callout" {
		if _, err := s.iconValue(payload["icon"], true); err != nil {
			return err
		}
	}
	if typ == "synced_block" {
		if sf, ok := payload["synced_from"].(map[string]any); ok && sf != nil {
			id := api.ExpandID(fmt.Sprint(sf["block_id"]))
			if orig := s.block(id); orig == nil || orig.raw["type"] != "synced_block" {
				return notFound(id)
			}
		}
	}
	if typ == "link_to_page" {
		for _, k := range []string{"page_id", "database_id"} {
			if id, ok := payload[k].(string); ok {
				if s.objects[api.ExpandID(id)] == nil {
					return notFound(id)
				}
			}
		}
	}
	children, _ := payload["children"].([]any)
	if len(children) > 0 {
		if depth >= 2 {
			return bad("blocks can only be nested two levels deep in one request")
		}
		if len(children) > 100 {
			return bad("children must have at most 100 items")
		}
		for _, c := range children {
			if err := s.validateBlock(c, depth+1); err != nil {
				return err
			}
			ct, _ := c.(map[string]any)["type"].(string)
			if typ == "column_list" && ct != "column" {
				return bad("column_list children must be columns")
			}
			if typ == "table" && ct != "table_row" {
				return bad("table children must be table rows")
			}
			if ct == "column" && typ != "column_list" {
				return bad("columns must be children of a column_list")
			}
		}
	}
	switch typ {
	case "column_list":
		if len(children) < 2 {
			return bad("column_list needs at least two columns")
		}
	case "column":
		if len(children) < 1 {
			return bad("a column needs at least one child")
		}
	case "table":
		if _, ok := payload["table_width"].(float64); !ok {
			return bad("table needs a table_width")
		}
		if len(children) < 1 {
			return bad("a table needs at least one row")
		}
		for _, c := range children {
			cells, _ := c.(map[string]any)["table_row"].(map[string]any)["cells"].([]any)
			if len(cells) != int(payload["table_width"].(float64)) {
				return bad("table rows must have table_width cells")
			}
		}
	case "table_row":
		if depth == 0 {
			return bad("table rows must be children of a table")
		}
	}
	return nil
}

// materialize stores a validated block (and its nested children) under
// parentID and returns the new id.
func (s *Server) materialize(parentID string, b map[string]any, after string) string {
	typ, _ := b["type"].(string)
	if typ == "" {
		for k := range b {
			if k != "object" {
				typ = k
			}
		}
	}
	src := b[typ].(map[string]any)
	payload := map[string]any{}
	for k, v := range src {
		if k == "children" {
			continue
		}
		payload[k] = v
	}
	for _, k := range []string{"rich_text", "caption"} {
		if rt, ok := payload[k]; ok {
			payload[k] = s.storeRichText(rt)
		}
	}
	if typ == "table_row" {
		cells, _ := payload["cells"].([]any)
		var out []any
		for _, c := range cells {
			out = append(out, s.storeRichText(c))
		}
		payload["cells"] = out
	}
	if mediaTypes[typ] {
		stored, _ := s.fileValue(payload)
		for k, v := range stored {
			payload[k] = v
		}
		delete(payload, "file_upload")
		if stored["type"] == "file" {
			delete(payload, "external")
		}
	}
	if typ == "callout" {
		icon, _ := s.iconValue(payload["icon"], true)
		payload["icon"] = icon
	}
	if typ == "synced_block" {
		if sf, ok := payload["synced_from"].(map[string]any); ok && sf != nil {
			payload["synced_from"] = map[string]any{"type": "block_id", "block_id": api.ExpandID(fmt.Sprint(sf["block_id"]))}
		} else {
			payload["synced_from"] = nil
		}
	}
	id := s.addBlock(parentID, typ, payload, after)
	children, _ := src["children"].([]any)
	for _, c := range children {
		s.materialize(id, c.(map[string]any), "")
	}
	return id
}

func (s *Server) appendChildren(r *http.Request, m []string, body map[string]any) (any, error) {
	o := s.lookup(m[1])
	if o == nil || (o.kind != "page" && o.kind != "block") {
		return nil, notFound(m[1])
	}
	children, _ := body["children"].([]any)
	if len(children) == 0 || len(children) > 100 {
		return nil, bad("children must have between 1 and 100 items")
	}
	if o.kind == "block" {
		switch o.raw["type"] {
		case "table":
			for _, c := range children {
				if c.(map[string]any)["type"] != "table_row" {
					return nil, bad("only table rows can be appended to a table")
				}
			}
		case "column_list":
			for _, c := range children {
				if c.(map[string]any)["type"] != "column" {
					return nil, bad("only columns can be appended to a column list")
				}
			}
		}
	}
	for _, c := range children {
		depth := 0
		if o.kind == "block" && (o.raw["type"] == "table" || o.raw["type"] == "column_list") {
			depth = 1
		}
		if err := s.validateBlock(c, depth); err != nil {
			return nil, err
		}
	}
	after := ""
	if pos, ok := body["position"].(map[string]any); ok {
		switch pos["type"] {
		case "start":
			after = "start"
		case "end", nil:
		case "after_block":
			ab, _ := pos["after_block"].(map[string]any)
			after = api.ExpandID(fmt.Sprint(ab["id"]))
		}
	}
	var created []any
	for _, c := range children {
		id := s.materialize(m[1], c.(map[string]any), after)
		if after != "" && after != "start" {
			after = id
		}
		if after == "start" {
			after = id
		}
		created = append(created, s.render(s.block(id).raw))
	}
	return map[string]any{"object": "list", "type": "block", "block": map[string]any{}, "results": created, "has_more": false, "next_cursor": nil}, nil
}

func (s *Server) deleteBlock(r *http.Request, m []string, body map[string]any) (any, error) {
	b := s.block(m[1])
	if b == nil {
		return nil, notFound(m[1])
	}
	b.raw["in_trash"] = true
	b.raw["archived"] = true
	if parent := s.lookup(b.parentID); parent != nil {
		for i, c := range parent.children {
			if c == m[1] {
				parent.children = append(parent.children[:i], parent.children[i+1:]...)
				break
			}
		}
	}
	return s.render(b.raw), nil
}

// --- markdown ---

func (s *Server) getMarkdown(r *http.Request, m []string, body map[string]any) (any, error) {
	root := s.lookup(m[1])
	if root == nil || (root.kind != "page" && root.kind != "block") {
		return nil, notFound(m[1])
	}
	if v := r.URL.Query().Get("markdown_version"); v != "v2" {
		return nil, bad("markdown_version must be v2")
	}
	var b strings.Builder
	var unknown []string
	count := 0
	ids := root.children
	if root.kind == "block" {
		// a subtree left out of a page: the block itself and below
		ids = []string{m[1]}
	}
	s.renderBlocks(&b, m[1], ids, 0, &count, &unknown)
	return map[string]any{
		"object":            "page_markdown",
		"id":                m[1],
		"markdown":          b.String(),
		"truncated":         len(unknown) > 0,
		"unknown_block_ids": unknown,
	}, nil
}

func (s *Server) renderBlocks(b *strings.Builder, root string, ids []string, depth int, count *int, unknown *[]string) {
	for _, id := range ids {
		o := s.lookup(id)
		blk := s.block(id)
		*count++
		if s.MarkdownLimit > 0 && *count > s.MarkdownLimit {
			fmt.Fprintf(b, "<unknown url=\"https://www.notion.so/%s#%s\" />\n\n", api.CompactID(root), api.CompactID(id))
			*unknown = append(*unknown, id)
			continue
		}
		typ := blk.raw["type"].(string)
		payload, _ := blk.raw[typ].(map[string]any)
		txt := plain(payload["rich_text"])
		switch typ {
		case "child_page":
			fmt.Fprintf(b, "<page url=\"https://www.notion.so/%s-%s\">%s</page>\n\n", slug(s.titleOf(o)), api.CompactID(id), s.titleOf(o))
			continue
		case "child_database":
			ds := ""
			if len(o.children) > 0 {
				ds = o.children[0]
			}
			fmt.Fprintf(b, "<database url=\"https://www.notion.so/%s\" data-source-url=\"https://www.notion.so/%s\" inline=\"false\">%s</database>\n\n", api.CompactID(id), api.CompactID(ds), plain(o.raw["title"]))
			continue
		case "heading_1":
			fmt.Fprintf(b, "# %s\n\n", txt)
		case "heading_2":
			fmt.Fprintf(b, "## %s\n\n", txt)
		case "heading_3":
			fmt.Fprintf(b, "### %s\n\n", txt)
		case "bulleted_list_item":
			fmt.Fprintf(b, "- %s\n\n", txt)
		case "numbered_list_item":
			fmt.Fprintf(b, "1. %s\n\n", txt)
		case "to_do":
			fmt.Fprintf(b, "- [ ] %s\n\n", txt)
		case "quote":
			fmt.Fprintf(b, "> %s\n\n", txt)
		case "callout":
			fmt.Fprintf(b, "<callout icon=\"💡\">%s</callout>\n\n", txt)
		case "toggle":
			fmt.Fprintf(b, "<toggle>%s\n\n", txt)
		case "code":
			fmt.Fprintf(b, "```%v\n%s\n```\n\n", payload["language"], txt)
		case "divider":
			b.WriteString("---\n\n")
		case "image":
			fmt.Fprintf(b, "![%s](%s)\n\n", plain(payload["caption"]), s.mediaURL(payload))
		case "file", "pdf", "video", "audio":
			fmt.Fprintf(b, "<%s source=\"%s\">%s</%s>\n\n", typ, s.mediaURL(payload), plain(payload["caption"]), typ)
		case "bookmark", "embed":
			fmt.Fprintf(b, "<%s url=\"%v\">%s</%s>\n\n", typ, payload["url"], plain(payload["caption"]), typ)
		case "column_list":
			b.WriteString("<columns>\n")
		case "column":
			b.WriteString("<column>\n")
		case "table":
			b.WriteString("<table>\n")
		case "table_row":
			cells, _ := payload["cells"].([]any)
			var cs []string
			for _, c := range cells {
				cs = append(cs, plain(c))
			}
			fmt.Fprintf(b, "| %s |\n", strings.Join(cs, " | "))
		default:
			fmt.Fprintf(b, "<%s>%s</%s>\n\n", typ, txt, typ)
		}
		if len(blk.children) > 0 {
			s.renderBlocks(b, root, blk.children, depth+1, count, unknown)
		}
		switch typ {
		case "toggle":
			b.WriteString("</toggle>\n\n")
		case "column_list":
			b.WriteString("</columns>\n\n")
		case "column":
			b.WriteString("</column>\n")
		case "table":
			b.WriteString("</table>\n\n")
		}
	}
}

func (s *Server) mediaURL(payload map[string]any) string {
	if payload["type"] == "external" {
		return fmt.Sprint(payload["external"].(map[string]any)["url"])
	}
	f, _ := payload["file"].(map[string]any)
	u, _ := f["url"].(string)
	signed, _ := s.signed(u)
	return signed
}

func slug(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// --- comments ---

func (s *Server) listComments(r *http.Request, m []string, body map[string]any) (any, error) {
	id := api.ExpandID(r.URL.Query().Get("block_id"))
	if s.lookup(id) == nil {
		return nil, notFound(id)
	}
	var items []any
	for _, c := range s.comments[id] {
		items = append(items, s.render(c))
	}
	return paginate(r, body, items, "comment"), nil
}

func (s *Server) createComment(r *http.Request, m []string, body map[string]any) (any, error) {
	rt, ok := body["rich_text"]
	if !ok {
		return nil, bad("rich_text is required")
	}
	if err := validateRichText(rt); err != nil {
		return nil, err
	}
	parentID := ""
	discussion := ""
	if d, ok := body["discussion_id"].(string); ok && d != "" {
		discussion = api.ExpandID(d)
		for pid, cs := range s.comments {
			for _, c := range cs {
				if c["discussion_id"] == discussion {
					parentID = pid
				}
			}
		}
		if parentID == "" {
			return nil, notFound(discussion)
		}
		if _, ok := body["parent"]; ok {
			return nil, bad("parent and discussion_id are mutually exclusive")
		}
	} else {
		parent, _ := body["parent"].(map[string]any)
		for _, k := range []string{"page_id", "block_id"} {
			if v, ok := parent[k].(string); ok {
				parentID = api.ExpandID(v)
			}
		}
		if parentID == "" || s.lookup(parentID) == nil {
			return nil, bad("parent must be an existing page or block")
		}
	}
	var attachments []map[string]any
	if atts, ok := body["attachments"].([]any); ok {
		if len(atts) > 3 {
			return nil, bad("at most 3 attachments")
		}
		for _, a := range atts {
			am, _ := a.(map[string]any)
			if am["type"] != "file_upload" {
				return nil, bad("attachments must be file uploads")
			}
			stored, err := s.fileValue(map[string]any{"type": "file_upload", "file_upload": map[string]any{"id": am["file_upload_id"]}})
			if err != nil {
				return nil, err
			}
			attachments = append(attachments, stored)
		}
	}
	var display map[string]any
	if dn, ok := body["display_name"].(map[string]any); ok {
		switch dn["type"] {
		case "custom":
			display = map[string]any{"type": "custom", "resolved_name": dn["custom"].(map[string]any)["name"]}
		case "integration":
			display = map[string]any{"type": "integration", "resolved_name": "plakar"}
		case "user":
			display = map[string]any{"type": "user", "resolved_name": "plakar"}
		default:
			return nil, bad("bad display_name type")
		}
	}
	id, disc := s.addComment(parentID, discussion, s.actor(), s.storeRichText(rt).([]any), attachments, display)
	for _, c := range s.comments[parentID] {
		if c["id"] == id {
			_ = disc
			return s.render(c), nil
		}
	}
	return nil, errors.New("comment vanished")
}

// --- views ---

func (s *Server) listViews(r *http.Request, m []string, body map[string]any) (any, error) {
	dbID := api.ExpandID(r.URL.Query().Get("database_id"))
	dsID := api.ExpandID(r.URL.Query().Get("data_source_id"))
	if dbID == "" && dsID == "" {
		return nil, bad("database_id or data_source_id is required")
	}
	if dbID != "" && s.objects[dbID] == nil {
		return nil, notFound(dbID)
	}
	var items []any
	for _, id := range s.viewList {
		v := s.views[id]
		if dbID != "" && v["parent"].(map[string]any)["database_id"] != dbID {
			continue
		}
		if dsID != "" && v["data_source_id"] != dsID {
			continue
		}
		items = append(items, map[string]any{"object": "view", "id": id})
	}
	return paginate(r, body, items, "view"), nil
}

func (s *Server) getView(r *http.Request, m []string, body map[string]any) (any, error) {
	if v, ok := s.views[m[1]]; ok {
		return v, nil
	}
	return nil, notFound(m[1])
}

func (s *Server) createView(r *http.Request, m []string, body map[string]any) (any, error) {
	dsID := api.ExpandID(fmt.Sprint(body["data_source_id"]))
	ds := s.objects[dsID]
	if ds == nil || ds.kind != "data_source" {
		return nil, bad("data_source_id must be an existing data source")
	}
	dbID := api.ExpandID(fmt.Sprint(body["database_id"]))
	if ds.parentID != dbID {
		return nil, bad("database_id must own the data source")
	}
	name, _ := body["name"].(string)
	typ, _ := body["type"].(string)
	if name == "" || typ == "" {
		return nil, bad("name and type are required")
	}
	cfg, _ := body["configuration"].(map[string]any)
	if cfg != nil && cfg["type"] != typ {
		return nil, bad("configuration type must match the view type")
	}
	id := s.addView(dbID, dsID, name, typ, body["filter"], body["sorts"], cfg)
	return s.views[id], nil
}

// --- file uploads ---

func (s *Server) createUpload(r *http.Request, m []string, body map[string]any) (any, error) {
	mode, _ := body["mode"].(string)
	if mode == "" {
		mode = "single_part"
	}
	up := &upload{status: "pending", parts: map[int][]byte{}}
	up.filename, _ = body["filename"].(string)
	up.contentType, _ = body["content_type"].(string)
	switch mode {
	case "single_part":
	case "multi_part":
		n, ok := body["number_of_parts"].(float64)
		if !ok || n < 1 || n > 1000 || up.filename == "" {
			return nil, bad("multi_part needs number_of_parts and a filename")
		}
		up.nparts = int(n)
	default:
		return nil, bad("unsupported mode %q", mode)
	}
	id := s.newID()
	s.uploads[id] = up
	return s.uploadObject(id), nil
}

func (s *Server) uploadObject(id string) map[string]any {
	up := s.uploads[id]
	size := 0
	for _, p := range up.parts {
		size += len(p)
	}
	return map[string]any{
		"object": "file_upload", "id": id, "status": up.status,
		"filename": up.filename, "content_type": up.contentType, "content_length": size,
		"number_of_parts": map[string]any{"total": up.nparts, "sent": len(up.parts)},
	}
}

func (s *Server) sendUpload(r *http.Request, m []string, body map[string]any) (any, error) {
	up := s.uploads[m[1]]
	if up == nil {
		return nil, notFound(m[1])
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, bad("multipart: %v", err)
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return nil, bad("missing file field")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, bad("reading file: %v", err)
	}
	if up.filename == "" {
		up.filename = hdr.Filename
	}
	if up.contentType == "" {
		up.contentType = hdr.Header.Get("Content-Type")
	}
	if up.nparts == 0 {
		up.parts[0] = data
		up.status = "uploaded"
	} else {
		n, err := strconv.Atoi(r.FormValue("part_number"))
		if err != nil || n < 1 || n > up.nparts {
			return nil, bad("bad part_number")
		}
		if n < up.nparts && len(data) < 5<<20 {
			return nil, bad("parts must be at least 5 MiB")
		}
		if len(data) > 20<<20 {
			return nil, bad("parts must be at most 20 MiB")
		}
		up.parts[n] = data
	}
	return s.uploadObject(m[1]), nil
}

func (s *Server) completeUpload(r *http.Request, m []string, body map[string]any) (any, error) {
	up := s.uploads[m[1]]
	if up == nil {
		return nil, notFound(m[1])
	}
	if up.nparts == 0 {
		return nil, bad("complete is only for multi_part uploads")
	}
	if len(up.parts) != up.nparts {
		return nil, bad("not all parts have been sent")
	}
	up.status = "uploaded"
	return s.uploadObject(m[1]), nil
}
