package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Parent is where a page, database or block hangs in the workspace.
type Parent struct {
	Type       string `json:"type"`
	PageID     string `json:"page_id,omitempty"`
	DatabaseID string `json:"database_id,omitempty"`
	BlockID    string `json:"block_id,omitempty"`
	Workspace  bool   `json:"workspace,omitempty"`
}

// ID is the parent's id, empty for the workspace.
func (p Parent) ID() string {
	switch p.Type {
	case "page_id":
		return p.PageID
	case "database_id":
		return p.DatabaseID
	case "block_id":
		return p.BlockID
	}
	return ""
}

// Object is the envelope every Notion object shares. Raw keeps the whole
// answer, which is what a backup stores: the typed fields only drive the
// traversal.
type Object struct {
	Object         string    `json:"object"`
	ID             string    `json:"id"`
	Type           string    `json:"type,omitempty"`
	Parent         Parent    `json:"parent"`
	HasChildren    bool      `json:"has_children"`
	Archived       bool      `json:"archived"`
	InTrash        bool      `json:"in_trash"`
	CreatedTime    time.Time `json:"created_time"`
	LastEditedTime time.Time `json:"last_edited_time"`

	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the raw bytes next to the decoded envelope.
func (o *Object) UnmarshalJSON(data []byte) error {
	type plain Object
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*o = Object(p)
	o.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// Map decodes the raw object into a generic map.
func (o *Object) Map() (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(o.Raw, &m); err != nil {
		return nil, fmt.Errorf("decode %s %s: %w", o.Object, o.ID, err)
	}
	return m, nil
}

// List is one page of a paginated answer.
type List struct {
	Results    []Object `json:"results"`
	HasMore    bool     `json:"has_more"`
	NextCursor string   `json:"next_cursor"`
}

// Title is the human name of a page, database or child_page block, empty
// when it has none.
func (o *Object) Title() string {
	var m map[string]any
	if err := json.Unmarshal(o.Raw, &m); err != nil {
		return ""
	}
	return TitleOf(m)
}

// TitleOf is Title on an already decoded object.
func TitleOf(m map[string]any) string {
	switch m["object"] {
	case "database":
		if rt, ok := m["title"].([]any); ok {
			return PlainText(rt)
		}
	case "page":
		props, _ := m["properties"].(map[string]any)
		for _, v := range props {
			p, ok := v.(map[string]any)
			if !ok || p["type"] != "title" {
				continue
			}
			if rt, ok := p["title"].([]any); ok {
				return PlainText(rt)
			}
		}
	case "block":
		if typ, _ := m["type"].(string); typ == "child_page" || typ == "child_database" {
			if body, ok := m[typ].(map[string]any); ok {
				title, _ := body["title"].(string)
				return title
			}
		}
	}
	return ""
}

// PlainText joins a rich text array into a string.
func PlainText(richText []any) string {
	var sb strings.Builder
	for _, item := range richText {
		if m, ok := item.(map[string]any); ok {
			if s, ok := m["plain_text"].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

var (
	dashedID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	bareID   = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
)

// ErrNotAnID is returned when a string cannot be read as a Notion id.
var ErrNotAnID = errors.New("not a notion id")

// NormalizeID turns the dashed or bare form of an id into the dashed,
// lowercase form the API returns.
func NormalizeID(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if dashedID.MatchString(s) {
		return s, nil
	}
	if bareID.MatchString(s) {
		return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
	}
	return "", fmt.Errorf("%w: %q", ErrNotAnID, s)
}

// ParseLocation reads the optional object id out of a connector location:
// "notion://" names the whole workspace, "notion://<id>" one page or
// database, and a pasted Notion URL such as
// "notion://www.notion.so/Title-<id>" is accepted too.
func ParseLocation(location string) (string, error) {
	rest := location
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return "", nil
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		rest = rest[i+1:]
	}
	if id, err := NormalizeID(rest); err == nil {
		return id, nil
	}
	if i := strings.LastIndex(rest, "-"); i >= 0 {
		if id, err := NormalizeID(rest[i+1:]); err == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("location %q: %w", location, ErrNotAnID)
}
