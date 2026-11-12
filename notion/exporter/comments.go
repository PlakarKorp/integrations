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
	"sort"

	"github.com/PlakarKorp/integrations/notion/api"
)

// restoreComments recreates the open discussions of a page.  Comments
// are written by the integration, so the original author is kept as
// the display name; replies land in the recreated discussion.
func (p *Exporter) restoreComments(ctx context.Context, e *entity, comments []map[string]any) error {
	sort.SliceStable(comments, func(i, j int) bool {
		a, _ := comments[i]["created_time"].(string)
		b, _ := comments[j]["created_time"].(string)
		return a < b
	})
	discussions := map[string]string{}

	for _, c := range comments {
		id, _ := c["id"].(string)
		rt := p.richText(anyList(c["rich_text"]))
		if len(rt) == 0 {
			rt = []any{map[string]any{"type": "text", "text": map[string]any{"content": "(attachment)"}}}
		}
		body := map[string]any{"rich_text": rt}

		disc, _ := c["discussion_id"].(string)
		if newDisc, ok := discussions[disc]; ok && newDisc != "" {
			body["discussion_id"] = newDisc
		} else {
			parent, _ := c["parent"].(map[string]any)
			target := map[string]any{"page_id": e.newID}
			if bid, ok := parent["block_id"].(string); ok {
				if n, ok := e.blockMap[bid]; ok {
					target = map[string]any{"block_id": n}
				} else if n, ok := p.blocks[bid]; ok {
					target = map[string]any{"block_id": n}
				}
			}
			body["parent"] = target
		}

		if dn, ok := c["display_name"].(map[string]any); ok {
			if name, ok := dn["resolved_name"].(string); ok && name != "" {
				body["display_name"] = map[string]any{"type": "custom", "custom": map[string]any{"name": name}}
			}
		}

		var attachments []any
		for _, a := range anyList(c["attachments"]) {
			am, _ := a.(map[string]any)
			f, _ := am["file"].(map[string]any)
			u, _ := f["url"].(string)
			key, ok := api.AssetKey(u)
			if !ok {
				continue
			}
			up, ok := e.uploads[key]
			if !ok {
				continue
			}
			attachments = append(attachments, map[string]any{"type": "file_upload", "file_upload_id": up.id})
			if len(attachments) == 3 {
				break
			}
		}
		if len(attachments) > 0 {
			body["attachments"] = attachments
		}

		resp, err := p.client.CreateComment(ctx, body)
		if err != nil && api.IsValidation(err) && len(attachments) > 0 {
			delete(body, "attachments")
			resp, err = p.client.CreateComment(ctx, body)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.warnf("%s: comment %s not restored: %v", e.dir, id, err)
			continue
		}
		var created struct {
			DiscussionID string `json:"discussion_id"`
		}
		if json.Unmarshal(resp, &created) == nil && created.DiscussionID != "" {
			discussions[disc] = created.DiscussionID
		}
	}
	return nil
}
