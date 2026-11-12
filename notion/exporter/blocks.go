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
	"context"
	"encoding/json"
	"fmt"

	"github.com/PlakarKorp/integrations/notion/api"
)

const (
	maxBatch      = 100
	maxBatchBytes = 400 << 10
)

// node is a block of the backup with its children.
type node struct {
	id       string
	typ      string
	raw      map[string]any
	children []*node
}

func parseTree(items []map[string]any) []*node {
	var out []*node
	for _, b := range items {
		n := &node{raw: b}
		n.id, _ = b["id"].(string)
		n.typ, _ = b["type"].(string)
		if n.typ == "" {
			continue
		}
		switch c := b["children"].(type) {
		case []any:
			var items []map[string]any
			for _, it := range c {
				if m, ok := it.(map[string]any); ok {
					items = append(items, m)
				}
			}
			n.children = parseTree(items)
		case []map[string]any:
			n.children = parseTree(c)
		}
		out = append(out, n)
	}
	return out
}

// skipTypes cannot be created through the API.
var skipTypes = map[string]bool{
	"unsupported": true, "template": true, "meeting_notes": true, "transcription": true,
}

var mediaTypes = map[string]bool{"file": true, "image": true, "video": true, "pdf": true, "audio": true}

type prepared struct {
	node   *node
	body   map[string]any
	inline bool // children were sent along (tables, column lists)
	// columns (by index among the columns) whose content could not go
	// along, because it holds a table or another column list and the
	// API takes two levels of nesting at most; they went with an empty
	// paragraph instead and get their content once their ids are known
	deferred map[int][]*node
}

// inlineTypes are the blocks whose children go in the same request.
var inlineTypes = map[string]bool{"table": true, "column_list": true}

func (p *Exporter) restoreBlocks(ctx context.Context, e *entity, tree []map[string]any) error {
	nodes := parseTree(tree)
	for _, n := range nodes {
		e.siblings = append(e.siblings, n.id)
	}
	return p.appendNodes(ctx, e, e.newID, nodes)
}

// appendNodes creates the blocks of a list under parentNew, in order,
// in batches, then descends into their children.
func (p *Exporter) appendNodes(ctx context.Context, e *entity, parentNew string, nodes []*node) error {
	var batch []prepared
	size := 0

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := p.appendBatch(ctx, e, parentNew, batch)
		batch, size = nil, 0
		return err
	}

	for _, n := range nodes {
		if n.typ == "child_page" || n.typ == "child_database" {
			// restored as objects of their own, in place, later
			continue
		}
		if skipTypes[n.typ] {
			p.notef("%s: block %s of type %s cannot be restored through the API", e.dir, n.id, n.typ)
			continue
		}
		pr, ok := p.prepare(e, n)
		if !ok {
			continue
		}
		sz := len(mustJSON(pr.body))
		if len(batch) >= maxBatch || (size+sz > maxBatchBytes && len(batch) > 0) {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, pr)
		size += sz
	}
	return flush()
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

// appendBatch sends one batch and, when the API refuses it, retries
// the blocks one by one so that a single bad block costs only itself.
func (p *Exporter) appendBatch(ctx context.Context, e *entity, parentNew string, batch []prepared) error {
	bodies := make([]any, len(batch))
	for i, pr := range batch {
		bodies[i] = pr.body
	}
	type created struct {
		pr    prepared
		newID string
	}
	var done []created

	results, err := p.client.AppendChildren(ctx, parentNew, bodies, nil)
	switch {
	case err == nil && len(results) != len(batch):
		// the blocks exist, we just cannot tell which is which: going
		// on would duplicate or misplace content
		return fmt.Errorf("%s: appended %d blocks, the API reports %d", e.dir, len(batch), len(results))
	case err == nil:
		for i, pr := range batch {
			done = append(done, created{pr, idOf(results[i])})
		}
	default:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !api.IsValidation(err) && !api.IsNotFound(err) {
			return fmt.Errorf("%s: appending blocks: %w", e.dir, err)
		}
		for _, pr := range batch {
			newID, err := p.appendOne(ctx, e, parentNew, pr)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				p.warnf("%s: block %s (%s) not restored: %v", e.dir, pr.node.id, pr.node.typ, err)
				continue
			}
			done = append(done, created{pr, newID})
		}
	}

	for _, c := range done {
		if c.newID == "" {
			continue
		}
		p.blocks[c.pr.node.id] = c.newID
		e.blockMap[c.pr.node.id] = c.newID
		if c.pr.inline {
			if err := p.inlineChildren(ctx, e, c.pr, c.newID); err != nil {
				return err
			}
			continue
		}
		if len(c.pr.node.children) > 0 {
			if err := p.appendNodes(ctx, e, c.newID, c.pr.node.children); err != nil {
				return err
			}
		}
	}
	return nil
}

// appendOne creates a single block; a synced block whose original is
// out of reach becomes an original of its own.
func (p *Exporter) appendOne(ctx context.Context, e *entity, parentNew string, pr prepared) (string, error) {
	results, err := p.client.AppendChildren(ctx, parentNew, []any{pr.body}, nil)
	if err != nil && pr.node.typ == "synced_block" {
		if sb, ok := pr.body["synced_block"].(map[string]any); ok && sb["synced_from"] != nil {
			pr.body["synced_block"] = map[string]any{"synced_from": nil}
			results, err = p.client.AppendChildren(ctx, parentNew, []any{pr.body}, nil)
		}
	}
	if err != nil {
		return "", err
	}
	if len(results) != 1 {
		return "", fmt.Errorf("expected one created block, got %d", len(results))
	}
	return idOf(results[0]), nil
}

func idOf(raw json.RawMessage) string {
	var v struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.ID
}

// prepare rewrites a backed up block into a request.
func (p *Exporter) prepare(e *entity, n *node) (prepared, bool) {
	typ := n.typ
	payload := map[string]any{}
	if src, ok := n.raw[typ].(map[string]any); ok {
		for k, v := range src {
			payload[k] = v
		}
	}
	delete(payload, "children")
	// the API answers with null for what is unset (a paragraph's icon,
	// say) and refuses null back
	for k, v := range payload {
		if v == nil {
			delete(payload, k)
		}
	}
	for _, k := range []string{"rich_text", "caption"} {
		if v, ok := payload[k]; ok {
			payload[k] = p.richText(anyList(v))
		}
	}

	pr := prepared{node: n}
	switch {
	case mediaTypes[typ]:
		ref, ok := p.fileRef(e, payload)
		if !ok {
			p.warnf("%s: block %s (%s): file not in the backup, block dropped", e.dir, n.id, typ)
			return pr, false
		}
		np := map[string]any{"type": ref["type"], ref["type"].(string): ref[ref["type"].(string)]}
		if c, ok := payload["caption"]; ok {
			np["caption"] = c
		}
		if name, ok := payload["name"].(string); ok && typ == "file" && name != "" {
			np["name"] = name
		}
		payload = np

	case typ == "link_preview":
		typ = "bookmark"
		payload = map[string]any{"url": payload["url"]}

	case typ == "callout":
		if icon, ok := payload["icon"].(map[string]any); ok {
			if icon["type"] == "file" {
				delete(payload, "icon")
			} else if ref := iconRef(icon); ref != nil {
				payload["icon"] = ref
			} else {
				delete(payload, "icon")
			}
		}

	case typ == "synced_block":
		payload["synced_from"] = nil
		src, _ := n.raw[typ].(map[string]any)
		if sf, ok := src["synced_from"].(map[string]any); ok && sf != nil {
			orig, _ := sf["block_id"].(string)
			if restored, ok := p.blocks[orig]; ok {
				payload["synced_from"] = map[string]any{"block_id": restored}
			} else {
				// the original is not part of this restore: a copy
				// pointing at an arbitrary block would pull content
				// from wherever a crafted snapshot says
				p.notef("%s: synced block %s: original %s not restored, kept as a plain copy", e.dir, n.id, orig)
			}
		}

	case typ == "link_to_page":
		// only links to copies made in this restore: a crafted
		// snapshot must not plant links to arbitrary objects
		linked := false
		for _, k := range []string{"page_id", "database_id", "data_source_id"} {
			if id, ok := payload[k].(string); ok {
				if n, ok := p.pages[id]; ok {
					payload[k] = n
					linked = true
				} else if n, ok := p.databases[id]; ok {
					payload[k] = n
					linked = true
				} else if n, ok := p.dataSources[id]; ok {
					payload[k] = n
					linked = true
				}
			}
		}
		if !linked {
			p.notef("%s: block %s: link to a page not part of the restore, dropped", e.dir, n.id)
			return pr, false
		}

	case typ == "table_row":
		payload["cells"] = p.cells(payload["cells"])

	case typ == "table":
		var rows []any
		for i, r := range n.children {
			if r.typ != "table_row" || i >= maxBatch {
				continue
			}
			rp := map[string]any{}
			if src, ok := r.raw["table_row"].(map[string]any); ok {
				rp["cells"] = p.cells(src["cells"])
			}
			rows = append(rows, map[string]any{"object": "block", "type": "table_row", "table_row": rp})
		}
		width, _ := payload["table_width"].(float64)
		if width < 1 || width > 100 {
			// the API caps tables at 100 columns; a crafted width
			// must not size an allocation
			width = 1
			payload["table_width"] = width
		}
		if len(rows) == 0 {
			cells := make([]any, int(width))
			for i := range cells {
				cells[i] = []any{}
			}
			rows = []any{map[string]any{"object": "block", "type": "table_row", "table_row": map[string]any{"cells": cells}}}
		}
		payload["children"] = rows
		pr.inline = true

	case typ == "column_list":
		var cols []any
		for _, c := range n.children {
			if c.typ != "column" {
				continue
			}
			cp := map[string]any{}
			if src, ok := c.raw["column"].(map[string]any); ok {
				if wr, ok := src["width_ratio"]; ok && wr != nil {
					cp["width_ratio"] = wr
				}
			}
			var kids []any
			defer_ := false
			for _, k := range c.children {
				if inlineTypes[k.typ] {
					defer_ = true
					break
				}
			}
			if defer_ {
				if pr.deferred == nil {
					pr.deferred = map[int][]*node{}
				}
				pr.deferred[len(cols)] = c.children
			} else {
				for _, k := range c.children {
					if k.typ == "child_page" || k.typ == "child_database" || skipTypes[k.typ] {
						continue
					}
					kp, ok := p.prepare(e, k)
					if !ok {
						continue
					}
					// one level only: deeper children follow once ids are known
					kids = append(kids, kp.body)
				}
			}
			if len(kids) == 0 {
				kids = []any{emptyParagraph()}
			}
			cp["children"] = kids
			cols = append(cols, map[string]any{"object": "block", "type": "column", "column": cp})
		}
		for len(cols) < 2 {
			cols = append(cols, map[string]any{"object": "block", "type": "column", "column": map[string]any{"children": []any{emptyParagraph()}}})
		}
		payload = map[string]any{"children": cols}
		pr.inline = true
	}

	pr.body = map[string]any{"object": "block", "type": typ, typ: payload}
	return pr, true
}

func emptyParagraph() map[string]any {
	return map[string]any{"object": "block", "type": "paragraph", "paragraph": map[string]any{"rich_text": []any{}}}
}

func (p *Exporter) cells(v any) []any {
	var out []any
	for _, c := range anyList(v) {
		out = append(out, p.richText(anyList(c)))
	}
	if out == nil {
		out = []any{}
	}
	return out
}

// inlineChildren finishes a block whose first level of children went
// along with it: the rows a table could not take at once, and what the
// columns of a column list hold below their first level.
func (p *Exporter) inlineChildren(ctx context.Context, e *entity, pr prepared, newID string) error {
	n := pr.node
	switch n.typ {
	case "table":
		if len(n.children) <= maxBatch {
			return nil
		}
		return p.appendNodes(ctx, e, newID, n.children[maxBatch:])

	case "column_list":
		deeper := len(pr.deferred) > 0
		for _, c := range n.children {
			for _, k := range c.children {
				if len(k.children) > 0 {
					deeper = true
				}
			}
		}
		if !deeper {
			return nil
		}
		colIDs, err := p.childIDs(ctx, newID)
		if err != nil {
			return err
		}
		ci := 0
		for _, c := range n.children {
			if c.typ != "column" {
				continue
			}
			if ci >= len(colIDs) {
				break
			}
			colNew := colIDs[ci]
			kids, deferred := pr.deferred[ci]
			ci++
			p.blocks[c.id] = colNew
			e.blockMap[c.id] = colNew
			if deferred {
				// the column holds only the placeholder: add its
				// content after it, then drop the placeholder
				placeholder, err := p.childIDs(ctx, colNew)
				if err != nil {
					return err
				}
				if err := p.appendNodes(ctx, e, colNew, kids); err != nil {
					return err
				}
				for _, id := range placeholder {
					if err := p.client.DeleteBlock(ctx, id); err != nil {
						p.warnf("%s: column %s: placeholder paragraph left in place: %v", e.dir, c.id, err)
					}
				}
				continue
			}
			needs := false
			for _, k := range c.children {
				if len(k.children) > 0 {
					needs = true
				}
			}
			if !needs {
				continue
			}
			kidIDs, err := p.childIDs(ctx, colNew)
			if err != nil {
				return err
			}
			ki := 0
			for _, k := range c.children {
				if k.typ == "child_page" || k.typ == "child_database" || skipTypes[k.typ] {
					continue
				}
				if _, ok := p.prepare(e, k); !ok {
					continue
				}
				if ki >= len(kidIDs) {
					break
				}
				kidNew := kidIDs[ki]
				ki++
				p.blocks[k.id] = kidNew
				e.blockMap[k.id] = kidNew
				if len(k.children) > 0 {
					if err := p.appendNodes(ctx, e, kidNew, k.children); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (p *Exporter) childIDs(ctx context.Context, id string) ([]string, error) {
	var ids []string
	err := p.client.BlockChildren(ctx, id, func(raw json.RawMessage) error {
		if cid := idOf(raw); cid != "" {
			ids = append(ids, cid)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing created blocks of %s: %w", id, err)
	}
	return ids, nil
}
