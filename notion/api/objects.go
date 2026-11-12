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

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Parent is the parent object every page, database, data source, block
// and comment carries.
type Parent struct {
	Type         string `json:"type"`
	PageID       string `json:"page_id,omitempty"`
	DatabaseID   string `json:"database_id,omitempty"`
	DataSourceID string `json:"data_source_id,omitempty"`
	BlockID      string `json:"block_id,omitempty"`
	Workspace    bool   `json:"workspace,omitempty"`
}

func (p Parent) ID() string {
	switch p.Type {
	case "page_id":
		return p.PageID
	case "database_id":
		return p.DatabaseID
	case "data_source_id":
		return p.DataSourceID
	case "block_id":
		return p.BlockID
	}
	return ""
}

// Object is the subset of fields common to the objects we handle,
// decoded from the raw JSON that is kept verbatim in Raw.
type Object struct {
	Object         string                     `json:"object"`
	ID             string                     `json:"id"`
	Type           string                     `json:"type"`
	Parent         Parent                     `json:"parent"`
	CreatedTime    time.Time                  `json:"created_time"`
	LastEditedTime time.Time                  `json:"last_edited_time"`
	InTrash        bool                       `json:"in_trash"`
	HasChildren    bool                       `json:"has_children"`
	Properties     map[string]json.RawMessage `json:"properties"`
	Title          []RichText                 `json:"title"`
	DataSources    []DataSourceRef            `json:"data_sources"`
	URL            string                     `json:"url"`
	CreatedBy      User                       `json:"created_by"`
	DiscussionID   string                     `json:"discussion_id"`

	Raw json.RawMessage `json:"-"`
}

// DataSourceRef names a data source of a database.
type DataSourceRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User is the part of a user object we read.
type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// RichText is the part of a rich text item we read.
type RichText struct {
	Type      string `json:"type"`
	PlainText string `json:"plain_text"`
}

// PlainText flattens a rich text array.
func PlainText(rt []RichText) string {
	var b strings.Builder
	for _, r := range rt {
		b.WriteString(r.PlainText)
	}
	return b.String()
}

// ParseObject decodes the common fields of an API object and keeps the
// raw JSON.
func ParseObject(raw json.RawMessage) (*Object, error) {
	var o Object
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("decoding object: %w", err)
	}
	if o.ID == "" {
		return nil, errors.New("decoding object: no id")
	}
	o.Raw = raw
	return &o, nil
}

// TitleText returns the human title of a page, a database, a data source
// or a child_page / child_database block.
func (o *Object) TitleText() string {
	switch o.Object {
	case "page":
		for _, p := range o.Properties {
			var v struct {
				Type  string     `json:"type"`
				Title []RichText `json:"title"`
			}
			if json.Unmarshal(p, &v) == nil && v.Type == "title" {
				return PlainText(v.Title)
			}
		}
		return ""
	case "block":
		var payload map[string]json.RawMessage
		if json.Unmarshal(o.Raw, &payload) == nil {
			var v struct {
				Title string `json:"title"`
			}
			if json.Unmarshal(payload[o.Type], &v) == nil {
				return v.Title
			}
		}
		return ""
	default:
		return PlainText(o.Title)
	}
}

// TitlePropertyName is the key of the title property of a page.
func (o *Object) TitlePropertyName() string {
	for name, p := range o.Properties {
		var v struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(p, &v) == nil && v.Type == "title" {
			return name
		}
	}
	return ""
}

var (
	compactIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	dashedIDRe  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// CompactID strips the dashes of a Notion id.
func CompactID(id string) string {
	return strings.ToLower(strings.ReplaceAll(id, "-", ""))
}

// ExpandID puts the dashes back.
func ExpandID(id string) string {
	id = CompactID(id)
	if len(id) != 32 {
		return id
	}
	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}

// IsID tells whether s looks like a Notion id, with or without dashes.
func IsID(s string) bool {
	return compactIDRe.MatchString(s) || dashedIDRe.MatchString(strings.ToLower(s))
}

// IDFromURL extracts the object id a notion.so link points at, as
// pages, databases and data sources are referenced in markdown.
func IDFromURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	p := strings.TrimSuffix(u.Path, "/")
	base := path.Base(p)
	if i := strings.LastIndex(base, "-"); i >= 0 && len(base)-i-1 == 32 {
		base = base[i+1:]
	}
	if compactIDRe.MatchString(base) {
		return ExpandID(base)
	}
	if dashedIDRe.MatchString(base) {
		return base
	}
	return ""
}

// FileRef is a Notion-hosted file found in an object: a signed url that
// expires, plus a stable key derived from the storage path.
type FileRef struct {
	URL  string
	Name string
	Key  string
}

var (
	fileHostsMu sync.Mutex
	fileHosts   = map[string]bool{}
)

// RegisterFileHost adds a host serving Notion-hosted files, for tests
// against a fake API; New does it for a non-default base url.
func RegisterFileHost(host string) {
	fileHostsMu.Lock()
	defer fileHostsMu.Unlock()
	fileHosts[strings.ToLower(host)] = true
}

// IsNotionFileURL tells whether a url points at Notion's file storage.
func IsNotionFileURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Host)
	fileHostsMu.Lock()
	extra := fileHosts[h]
	fileHostsMu.Unlock()
	switch {
	case extra:
		return true
	case h == "file.notion.so":
		return true
	case strings.HasPrefix(h, "prod-files-secure.") && strings.HasSuffix(h, ".amazonaws.com"):
		return true
	case strings.HasSuffix(h, ".amazonaws.com") && strings.Contains(u.Path, "secure.notion-static.com"):
		return true
	}
	return false
}

// AssetKey turns a signed url into "<file id>/<file name>", which is
// stable across re-signings and unique per uploaded file.
func AssetKey(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil {
		return "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 {
		return "", false
	}
	id := segs[len(segs)-2]
	name, err := url.PathUnescape(segs[len(segs)-1])
	if err != nil || name == "" {
		name = segs[len(segs)-1]
	}
	if !IsID(id) {
		return "", false
	}
	return CompactID(id) + "/" + safeName(name), true
}

func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[len(r)-200:])
	}
	return name
}

// FileRefs walks a decoded JSON document and returns every Notion-hosted
// file it references: block files, icons, covers, files properties and
// comment attachments all share the {"url", "expiry_time"} shape.
func FileRefs(raw json.RawMessage) []FileRef {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	var refs []FileRef
	seen := map[string]struct{}{}
	walkFiles(doc, func(ref FileRef) {
		if _, ok := seen[ref.Key]; ok {
			return
		}
		seen[ref.Key] = struct{}{}
		refs = append(refs, ref)
	})
	return refs
}

func walkFiles(v any, emit func(FileRef)) {
	switch t := v.(type) {
	case map[string]any:
		if f, ok := t["file"].(map[string]any); ok {
			if u, ok := f["url"].(string); ok && u != "" {
				if _, ok := f["expiry_time"]; ok || IsNotionFileURL(u) {
					if key, ok := AssetKey(u); ok {
						name, _ := t["name"].(string)
						if name == "" {
							name = path.Base(key)
						}
						emit(FileRef{URL: u, Name: name, Key: key})
					}
				}
			}
		}
		for _, c := range t {
			walkFiles(c, emit)
		}
	case []any:
		for _, c := range t {
			walkFiles(c, emit)
		}
	}
}
