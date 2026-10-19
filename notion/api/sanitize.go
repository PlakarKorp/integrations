package api

// The API hands back more than it accepts: ids, timestamps, authors,
// computed values and expiring file links come with every object but are
// rejected when sent to a create endpoint. These helpers turn an object as
// backed up into the body that recreates it.

// IDMap resolves an id of the backed-up workspace to the id of the object
// restored in its place, when there is one.
type IDMap func(old string) (string, bool)

// blocksNotCreatable are block types the API only ever reads. child_page
// and child_database are restored as pages and databases, the others are
// lost and the caller reports them.
var blocksNotCreatable = map[string]bool{
	"child_page":     true,
	"child_database": true,
	"unsupported":    true,
	"link_preview":   true,
	"template":       true,
}

// blocksWithInlineChildren must be created together with their children.
var blocksWithInlineChildren = map[string]bool{
	"column_list": true,
	"column":      true,
	"table":       true,
}

// fileBlocks carry a file as their payload.
var fileBlocks = map[string]bool{
	"image": true,
	"video": true,
	"audio": true,
	"file":  true,
	"pdf":   true,
}

// NeedsInlineChildren reports whether blocks of this type are only accepted
// with their children in the same request.
func NeedsInlineChildren(typ string) bool { return blocksWithInlineChildren[typ] }

// IsFileBlock reports whether blocks of this type embed a file.
func IsFileBlock(typ string) bool { return fileBlocks[typ] }

// BlockForCreate returns the body that appends a copy of block, or false
// when the API cannot create that kind of block. The body has no children.
func BlockForCreate(block map[string]any, ids IDMap) (map[string]any, bool) {
	typ, _ := block["type"].(string)
	if typ == "" || blocksNotCreatable[typ] {
		return nil, false
	}
	payload, ok := block[typ].(map[string]any)
	if !ok {
		payload = map[string]any{}
	}
	payload = scrub(payload, ids).(map[string]any)
	delete(payload, "children")

	switch typ {
	case "callout":
		if IsHostedFile(payload["icon"]) {
			delete(payload, "icon")
		}
	case "link_to_page":
		remapTarget(payload, ids)
	case "synced_block":
		if from, ok := payload["synced_from"].(map[string]any); ok {
			remapTarget(from, ids)
		}
	}

	return map[string]any{"object": "block", "type": typ, typ: payload}, true
}

// IsHostedFile reports whether a file, icon or cover object points at a
// file Notion hosts, whose link expires and cannot be sent back.
func IsHostedFile(v any) bool {
	m, ok := v.(map[string]any)
	return ok && m["type"] == "file"
}

// HostedFileURL is the download link of a hosted file object, empty for
// anything else.
func HostedFileURL(v any) string {
	m, ok := v.(map[string]any)
	if !ok || m["type"] != "file" {
		return ""
	}
	f, _ := m["file"].(map[string]any)
	u, _ := f["url"].(string)
	return u
}

// SetFileUpload points a file, icon or cover object at an upload instead of
// the hosted file it was backed up with.
func SetFileUpload(m map[string]any, uploadID string) {
	delete(m, "file")
	delete(m, "external")
	m["type"] = "file_upload"
	m["file_upload"] = map[string]any{"id": uploadID}
}

// PageOptions shapes PageForCreate.
type PageOptions struct {
	// Parent is the parent object of the new page.
	Parent map[string]any
	// TitleOnly keeps the title and drops every other property. It is
	// what a page needs under a page parent, where only the title exists.
	TitleOnly bool
	// Drop names properties to leave out, those the database could not be
	// created with.
	Drop map[string]bool
	IDs  IDMap
}

// propertiesReadOnly are property value types the API computes itself.
var propertiesReadOnly = map[string]bool{
	"formula":          true,
	"rollup":           true,
	"created_time":     true,
	"created_by":       true,
	"last_edited_time": true,
	"last_edited_by":   true,
	"unique_id":        true,
	"button":           true,
	"verification":     true,
}

// PageForCreate returns the body that creates a copy of page. hostedIcon and
// hostedCover report an icon or cover Notion hosted, which the body leaves
// out and the caller may upload again.
func PageForCreate(page map[string]any, o PageOptions) (body map[string]any, hostedIcon, hostedCover bool) {
	body = map[string]any{"parent": o.Parent}
	body["properties"], _ = cleanProperties(page["properties"], o)

	if icon := page["icon"]; icon != nil {
		if IsHostedFile(icon) {
			hostedIcon = true
		} else {
			body["icon"] = scrub(icon, o.IDs)
		}
	}
	if cover := page["cover"]; cover != nil {
		if IsHostedFile(cover) {
			hostedCover = true
		} else {
			body["cover"] = scrub(cover, o.IDs)
		}
	}
	return body, hostedIcon, hostedCover
}

func cleanProperties(v any, o PageOptions) (map[string]any, bool) {
	props, ok := v.(map[string]any)
	if !ok {
		return map[string]any{}, false
	}
	out := make(map[string]any, len(props))
	for name, pv := range props {
		p, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := p["type"].(string)
		if o.TitleOnly {
			if typ == "title" {
				out["title"] = map[string]any{"title": scrub(p["title"], o.IDs)}
			}
			continue
		}
		if propertiesReadOnly[typ] || o.Drop[name] {
			continue
		}
		value := scrub(p[typ], o.IDs)
		switch typ {
		case "select", "status":
			value = optionName(value)
		case "multi_select":
			if list, ok := value.([]any); ok {
				names := make([]any, 0, len(list))
				for _, item := range list {
					if n := optionName(item); n != nil {
						names = append(names, n)
					}
				}
				value = names
			}
		case "relation":
			list, _ := value.([]any)
			kept := make([]any, 0, len(list))
			for _, item := range list {
				m, _ := item.(map[string]any)
				id, _ := m["id"].(string)
				if o.IDs == nil {
					kept = append(kept, map[string]any{"id": id})
					continue
				}
				if nid, ok := o.IDs(id); ok {
					kept = append(kept, map[string]any{"id": nid})
				}
			}
			value = kept
		case "files":
			list, _ := value.([]any)
			kept := make([]any, 0, len(list))
			for _, item := range list {
				if !IsHostedFile(item) {
					kept = append(kept, item)
				}
			}
			value = kept
		case "date":
			if value == nil {
				continue
			}
		}
		out[name] = map[string]any{typ: value}
	}
	return out, true
}

// optionName reduces a select option to its name: option ids differ between
// workspaces and a colour that disagrees with the existing option is an
// error.
func optionName(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	name, _ := m["name"].(string)
	if name == "" {
		return nil
	}
	return map[string]any{"name": name}
}

// schemaNotCreatable are property kinds a database cannot be created with.
var schemaNotCreatable = map[string]bool{
	"rollup":       true,
	"status":       true,
	"unique_id":    true,
	"button":       true,
	"verification": true,
	"place":        true,
}

// DatabaseForCreate returns the body that creates a copy of db under parent,
// and the names of the properties it had to leave out so the rows leave
// them out too. Relations to databases ids does not know are dropped, the
// database they point at does not exist in the target.
func DatabaseForCreate(db map[string]any, parent map[string]any, ids IDMap) (body map[string]any, dropped map[string]bool, hostedIcon, hostedCover bool) {
	body = map[string]any{"parent": parent}
	dropped = map[string]bool{}

	for _, key := range []string{"title", "description", "is_inline"} {
		if v, ok := db[key]; ok && v != nil {
			body[key] = scrub(v, ids)
		}
	}
	if icon := db["icon"]; icon != nil {
		if IsHostedFile(icon) {
			hostedIcon = true
		} else {
			body["icon"] = scrub(icon, ids)
		}
	}
	if cover := db["cover"]; cover != nil {
		if IsHostedFile(cover) {
			hostedCover = true
		} else {
			body["cover"] = scrub(cover, ids)
		}
	}

	schema := map[string]any{}
	props, _ := db["properties"].(map[string]any)
	for name, pv := range props {
		p, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := p["type"].(string)
		if typ == "" || schemaNotCreatable[typ] {
			dropped[name] = true
			continue
		}
		def, _ := p[typ].(map[string]any)
		if def == nil {
			def = map[string]any{}
		}
		def = scrub(def, ids).(map[string]any)

		switch typ {
		case "select", "multi_select":
			if opts, ok := def["options"].([]any); ok {
				clean := make([]any, 0, len(opts))
				for _, o := range opts {
					if m, ok := o.(map[string]any); ok {
						item := map[string]any{"name": m["name"]}
						if c, ok := m["color"]; ok {
							item["color"] = c
						}
						clean = append(clean, item)
					}
				}
				def["options"] = clean
			}
		case "relation":
			target, _ := def["database_id"].(string)
			nid, ok := "", false
			if ids != nil {
				nid, ok = ids(target)
			}
			if !ok {
				dropped[name] = true
				continue
			}
			kind, _ := def["type"].(string)
			if kind == "" {
				kind = "single_property"
			}
			def = map[string]any{"database_id": nid, "type": kind, kind: map[string]any{}}
		}
		schema[name] = map[string]any{typ: def}
	}
	body["properties"] = schema
	return body, dropped, hostedIcon, hostedCover
}

// scrub walks a value and removes what the API computes: the derived
// fields of rich text, everything about a user but its id, and object ids
// the target workspace will not have.
func scrub(v any, ids IDMap) any {
	switch t := v.(type) {
	case map[string]any:
		if t["object"] == "user" {
			return map[string]any{"object": "user", "id": t["id"]}
		}
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = scrub(val, ids)
		}
		if _, isRichText := out["annotations"]; isRichText {
			delete(out, "plain_text")
			delete(out, "href")
			if m, ok := out["mention"].(map[string]any); ok {
				remapMention(m, ids)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = scrub(val, ids)
		}
		return out
	default:
		return v
	}
}

// remapMention points a page or database mention at the restored copy when
// there is one; the original may still exist, so an unknown id is kept.
func remapMention(m map[string]any, ids IDMap) {
	kind, _ := m["type"].(string)
	if kind != "page" && kind != "database" {
		return
	}
	target, ok := m[kind].(map[string]any)
	if !ok || ids == nil {
		return
	}
	if old, ok := target["id"].(string); ok {
		if nid, ok := ids(old); ok {
			target["id"] = nid
		}
	}
}

// remapTarget rewrites a {type: page_id|database_id|block_id, <type>: id}
// reference to the restored copy when there is one.
func remapTarget(m map[string]any, ids IDMap) {
	if ids == nil {
		return
	}
	kind, _ := m["type"].(string)
	if old, ok := m[kind].(string); ok {
		if nid, ok := ids(old); ok {
			m[kind] = nid
		}
	}
}
