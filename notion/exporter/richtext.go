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

package exporter

import (
	"github.com/PlakarKorp/integrations/notion/api"
)

const maxTextContent = 2000

// richText rewrites a rich text array the way the write endpoints
// want it: no plain_text or href, mentions pointing at the copies.
func (p *Exporter) richText(items []any) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if rt := p.richTextItem(m); rt != nil {
			out = append(out, rt)
		}
	}
	return out
}

func (p *Exporter) richTextItem(m map[string]any) map[string]any {
	typ, _ := m["type"].(string)
	plain, _ := m["plain_text"].(string)
	out := map[string]any{}
	if a, ok := m["annotations"].(map[string]any); ok {
		out["annotations"] = a
	}
	switch typ {
	case "text":
		t, _ := m["text"].(map[string]any)
		content, _ := t["content"].(string)
		if r := []rune(content); len(r) > maxTextContent {
			content = string(r[:maxTextContent])
		}
		text := map[string]any{"content": content}
		if l, ok := t["link"].(map[string]any); ok && l != nil {
			if u, ok := l["url"].(string); ok && u != "" {
				text["link"] = map[string]any{"url": p.rewriteLink(u)}
			}
		}
		out["type"] = "text"
		out["text"] = text
		return out
	case "equation":
		out["type"] = "equation"
		out["equation"] = m["equation"]
		return out
	case "mention":
		mention, _ := m["mention"].(map[string]any)
		if mm := p.mention(mention); mm != nil {
			out["type"] = "mention"
			out["mention"] = mm
			return out
		}
	}
	// anything else becomes its plain text
	if plain == "" {
		return nil
	}
	out["type"] = "text"
	out["text"] = map[string]any{"content": plain}
	if href, ok := m["href"].(string); ok && href != "" {
		out["text"].(map[string]any)["link"] = map[string]any{"url": href}
	}
	return out
}

// mention rewrites a mention; nil means it cannot be written and the
// caller should fall back to plain text.
func (p *Exporter) mention(m map[string]any) map[string]any {
	typ, _ := m["type"].(string)
	switch typ {
	case "user":
		u, _ := m["user"].(map[string]any)
		id, _ := u["id"].(string)
		if id == "" {
			return nil
		}
		return map[string]any{"type": "user", "user": map[string]any{"object": "user", "id": id}}
	case "page":
		pg, _ := m["page"].(map[string]any)
		id, _ := pg["id"].(string)
		if n, ok := p.pages[id]; ok {
			id = n
		}
		if id == "" {
			return nil
		}
		return map[string]any{"type": "page", "page": map[string]any{"id": id}}
	case "database":
		db, _ := m["database"].(map[string]any)
		id, _ := db["id"].(string)
		if n, ok := p.databases[id]; ok {
			id = n
		}
		if id == "" {
			return nil
		}
		return map[string]any{"type": "database", "database": map[string]any{"id": id}}
	case "data_source":
		ds, _ := m["data_source"].(map[string]any)
		id, _ := ds["id"].(string)
		if n, ok := p.dataSources[id]; ok {
			id = n
		}
		if id == "" {
			return nil
		}
		return map[string]any{"type": "data_source", "data_source": map[string]any{"id": id}}
	case "date", "template_mention", "custom_emoji":
		return map[string]any{"type": typ, typ: m[typ]}
	}
	return nil
}

// rewriteLink points a notion.so link at the copy of its target when
// the target was restored.
func (p *Exporter) rewriteLink(u string) string {
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
	return u
}

func anyList(v any) []any {
	arr, _ := v.([]any)
	return arr
}
