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
	"strings"

	"github.com/PlakarKorp/integrations/notion/api"
)

// restoreViews recreates the views of a database on the copies of its
// data sources.  Property ids differ in the copy, so every reference to
// one is rewritten by name; a view the API still refuses is created
// bare, with its name and type.
func (p *Exporter) restoreViews(ctx context.Context, e *entity, views []map[string]any) error {
	for _, v := range views {
		id, _ := v["id"].(string)
		dsOld, _ := v["data_source_id"].(string)
		dsNew, ok := p.dataSources[dsOld]
		if !ok {
			p.warnf("%s: view %s: its data source was not restored", e.dir, id)
			continue
		}
		name, _ := v["name"].(string)
		typ, _ := v["type"].(string)
		if name == "" || typ == "" || typ == "dashboard" {
			continue
		}
		base := map[string]any{
			"database_id":    e.newID,
			"data_source_id": dsNew,
			"name":           name,
			"type":           typ,
		}
		body := map[string]any{}
		for k, val := range base {
			body[k] = val
		}
		sc := p.schemas[dsNew]
		for _, k := range []string{"filter", "sorts", "quick_filters", "configuration"} {
			if val, ok := v[k]; ok && val != nil {
				body[k] = remapProperties(val, sc)
			}
		}
		_, err := p.client.CreateView(ctx, body)
		if err != nil && api.IsValidation(err) {
			p.warnf("%s: view %q: configuration not accepted, created bare: %v", e.dir, name, err)
			_, err = p.client.CreateView(ctx, base)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.warnf("%s: view %q not restored: %v", e.dir, name, err)
		}
	}
	return nil
}

// remapProperties walks a view's JSON and points property references
// at the copy's ids.
func remapProperties(v any, sc *schema) any {
	if sc == nil {
		return v
	}
	byOld := map[string]string{}
	for name, old := range sc.oldIDs {
		if n, ok := sc.newIDs[name]; ok {
			byOld[old] = n
		}
	}
	var walk func(v any, key string) any
	walk = func(v any, key string) any {
		switch t := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, c := range t {
				out[k] = walk(c, k)
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, c := range t {
				out[i] = walk(c, key)
			}
			return out
		case string:
			if key == "property" || key == "property_id" || strings.HasSuffix(key, "_property_id") {
				if n, ok := byOld[t]; ok {
					return n
				}
			}
			return t
		}
		return v
	}
	return walk(v, "")
}
