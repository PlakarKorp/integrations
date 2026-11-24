package exporter

import (
	"context"
	"strings"

	"github.com/PlakarKorp/integrations/notion/api"
)

// maxTextContent is the API's limit on one rich text item; a longer
// run is split over several items with the same annotations.
const maxTextContent = 2000

// richText rewrites a rich text array the way the write endpoints
// want it: no plain_text or href, mentions pointing at the copies.
// A mention of an object that is not restored yet becomes its plain
// text and counts in pendingRefs, so that the caller can come back
// once everything exists.
func (p *Exporter) richText(items []any) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		for _, rt := range p.richTextItem(m) {
			out = append(out, rt)
		}
	}
	return out
}

func (p *Exporter) richTextItem(m map[string]any) []map[string]any {
	typ, _ := m["type"].(string)
	plain, _ := m["plain_text"].(string)
	base := map[string]any{}
	if a, ok := m["annotations"].(map[string]any); ok {
		base["annotations"] = a
	}
	switch typ {
	case "text":
		t, _ := m["text"].(map[string]any)
		content, _ := t["content"].(string)
		var link map[string]any
		if l, ok := t["link"].(map[string]any); ok && l != nil {
			if u, ok := l["url"].(string); ok && u != "" {
				link = map[string]any{"url": p.rewriteLink(u)}
			}
		}
		var out []map[string]any
		for _, part := range splitRunes(content, maxTextContent) {
			item := map[string]any{"type": "text", "text": map[string]any{"content": part}}
			for k, v := range base {
				item[k] = v
			}
			if link != nil {
				item["text"].(map[string]any)["link"] = link
			}
			out = append(out, item)
		}
		return out
	case "equation":
		base["type"] = "equation"
		base["equation"] = m["equation"]
		return []map[string]any{base}
	case "mention":
		mention, _ := m["mention"].(map[string]any)
		if mm := p.mention(mention); mm != nil {
			base["type"] = "mention"
			base["mention"] = mm
			return []map[string]any{base}
		}
	}
	// anything else becomes its plain text
	if plain == "" {
		return nil
	}
	var out []map[string]any
	for _, part := range splitRunes(plain, maxTextContent) {
		item := map[string]any{"type": "text", "text": map[string]any{"content": part}}
		for k, v := range base {
			item[k] = v
		}
		if href, ok := m["href"].(string); ok && href != "" {
			item["text"].(map[string]any)["link"] = map[string]any{"url": p.rewriteLink(href)}
		}
		out = append(out, item)
	}
	return out
}

// splitRunes cuts s into pieces of at most n runes; an empty s yields
// one empty piece, which keeps an empty text item an item.
func splitRunes(s string, n int) []string {
	r := []rune(s)
	if len(r) <= n {
		return []string{s}
	}
	var out []string
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return append(out, string(r))
}

// mention rewrites a mention; nil means it cannot be written and the
// caller should fall back to plain text.
func (p *Exporter) mention(m map[string]any) map[string]any {
	typ, _ := m["type"].(string)
	switch typ {
	// mentions only point at members of the target workspace and at
	// copies made in this restore; anything else becomes plain text
	case "user":
		u, _ := m["user"].(map[string]any)
		id, _ := u["id"].(string)
		if id == "" || !p.knownUser(context.Background(), id) {
			return nil
		}
		return map[string]any{"type": "user", "user": map[string]any{"object": "user", "id": id}}
	case "page":
		pg, _ := m["page"].(map[string]any)
		id, _ := pg["id"].(string)
		n, ok := p.pages[id]
		if !ok {
			p.pending(id)
			return nil
		}
		return map[string]any{"type": "page", "page": map[string]any{"id": n}}
	case "database":
		db, _ := m["database"].(map[string]any)
		id, _ := db["id"].(string)
		n, ok := p.databases[id]
		if !ok {
			p.pending(id)
			return nil
		}
		return map[string]any{"type": "database", "database": map[string]any{"id": n}}
	case "data_source":
		ds, _ := m["data_source"].(map[string]any)
		id, _ := ds["id"].(string)
		n, ok := p.dataSources[id]
		if !ok {
			p.pending(id)
			return nil
		}
		return map[string]any{"type": "data_source", "data_source": map[string]any{"id": n}}
	case "date", "template_mention", "custom_emoji":
		return map[string]any{"type": typ, typ: m[typ]}
	}
	return nil
}

// pending notes a reference to an object that may still be restored
// later in the walk.
func (p *Exporter) pending(id string) {
	if api.IsID(id) {
		p.pendingRefs++
	}
}

// rewriteLink points a notion.so link at the copy of its target when
// the target was restored.  Notion hands internal links out as "/<id>",
// which the API refuses back: those become absolute.
func (p *Exporter) rewriteLink(u string) string {
	if strings.HasPrefix(u, "/") {
		u = "https://www.notion.so" + u
	}
	id := api.IDFromURL(u)
	if id == "" {
		return u
	}
	if n, ok := p.pages[id]; ok {
		return "https://www.notion.so/" + api.CompactID(n)
	}
	if n, ok := p.databases[id]; ok {
		return "https://www.notion.so/" + api.CompactID(n)
	}
	p.pending(id)
	return u
}

func anyList(v any) []any {
	arr, _ := v.([]any)
	return arr
}
