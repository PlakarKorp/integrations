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
	"sort"

	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/layout"
)

// schema is what we keep about a restored data source.
type schema struct {
	oldID string
	newID string
	// the schema as backed up, by property name
	old map[string]map[string]any
	// property ids in the copy, by name, and the old ids by name
	newIDs map[string]string
	oldIDs map[string]string
	// dual relations already created from the other side
	synced map[string]bool
}

// plainSchema lists the property types that need nothing else to exist.
var plainSchema = map[string]bool{
	"title": true, "rich_text": true, "number": true, "select": true, "multi_select": true,
	"status": true, "date": true, "people": true, "files": true, "checkbox": true, "url": true,
	"email": true, "phone_number": true, "created_time": true, "created_by": true,
	"last_edited_time": true, "last_edited_by": true, "unique_id": true, "place": true,
}

func oldSchema(obj *api.Object) map[string]map[string]any {
	out := map[string]map[string]any{}
	for name, raw := range obj.Properties {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			out[name] = m
		}
	}
	return out
}

// propertyConfig rewrites one property of the backed up schema into a
// request.  Options lose their ids, relations point at the copy of
// their target when it exists.
func propertyConfig(name string, m map[string]any) map[string]any {
	typ, _ := m["type"].(string)
	cfg := map[string]any{}
	if c, ok := m[typ].(map[string]any); ok {
		for k, v := range c {
			cfg[k] = v
		}
	}
	switch typ {
	case "select", "multi_select", "status":
		var opts []any
		for _, o := range anyList(cfg["options"]) {
			om, _ := o.(map[string]any)
			n, _ := om["name"].(string)
			if n == "" {
				continue
			}
			no := map[string]any{"name": n}
			if c, ok := om["color"].(string); ok && c != "" {
				no["color"] = c
			}
			if d, ok := om["description"].(string); ok && d != "" {
				no["description"] = d
			}
			opts = append(opts, no)
		}
		cfg = map[string]any{}
		if len(opts) > 0 {
			cfg["options"] = opts
		}
	case "number":
		format := cfg["format"]
		cfg = map[string]any{}
		if format != nil {
			cfg["format"] = format
		}
	case "unique_id":
		prefix, _ := cfg["prefix"].(string)
		cfg = map[string]any{}
		if prefix != "" {
			cfg["prefix"] = prefix
		}
	case "formula":
		cfg = map[string]any{"expression": cfg["expression"]}
	case "rollup":
		cfg = map[string]any{
			"relation_property_name": cfg["relation_property_name"],
			"rollup_property_name":   cfg["rollup_property_name"],
			"function":               cfg["function"],
		}
	case "relation":
		// rewritten by the relations phase
	default:
		cfg = map[string]any{}
	}
	out := map[string]any{"type": typ, typ: cfg}
	if d, ok := m["description"].(string); ok && d != "" {
		out["description"] = d
	}
	_ = name
	return out
}

// phaseOne is the part of a schema that can be created up front.
func phaseOne(old map[string]map[string]any) map[string]any {
	props := map[string]any{}
	for name, m := range old {
		typ, _ := m["type"].(string)
		if !plainSchema[typ] {
			continue
		}
		props[name] = propertyConfig(name, m)
	}
	return props
}

// createDatabase creates a database, with the schema of its first data
// source when that is known (ds), a bare title otherwise.
func (p *Exporter) createDatabase(ctx context.Context, e *entity, ds *entity) error {
	if e.created {
		return nil
	}
	parent, _, err := p.parentRef(ctx, e)
	if err != nil {
		return err
	}
	if parent["type"] != "page_id" {
		return fmt.Errorf("%s: a database can only be restored under a page", e.dir)
	}
	var raw map[string]any
	if err := json.Unmarshal(e.obj.Raw, &raw); err != nil {
		return err
	}
	body := map[string]any{
		"parent": parent,
		"title":  p.richText(anyList(raw["title"])),
	}
	if d := anyList(raw["description"]); len(d) > 0 {
		body["description"] = p.richText(d)
	}
	if v, ok := raw["is_inline"].(bool); ok {
		body["is_inline"] = v
	}
	if v, ok := raw["show_data_source_title"].(bool); ok {
		body["show_data_source_title"] = v
	}
	if icon, ok := raw["icon"].(map[string]any); ok {
		if icon["type"] == "file" {
			e.patch = true
		} else if ref := iconRef(icon); ref != nil {
			body["icon"] = ref
		}
	}
	if cover, ok := raw["cover"].(map[string]any); ok {
		if cover["type"] == "file" {
			e.patch = true
		} else if ref, ok := p.fileRef(e, cover); ok {
			body["cover"] = ref
		}
	}

	var sc *schema
	initial := map[string]any{"Name": map[string]any{"type": "title", "title": map[string]any{}}}
	if ds != nil {
		sc = &schema{oldID: ds.oldID, old: oldSchema(ds.obj), newIDs: map[string]string{}, oldIDs: map[string]string{}, synced: map[string]bool{}}
		initial = phaseOne(sc.old)
	}
	body["initial_data_source"] = map[string]any{"properties": initial}

	resp, err := p.client.CreateDatabase(ctx, body)
	if err != nil && api.IsValidation(err) && ds != nil {
		p.warnf("%s: schema not accepted, restoring it property by property: %v", ds.dir, err)
		title := map[string]any{}
		for name, cfg := range initial {
			if cfg.(map[string]any)["type"] == "title" {
				title[name] = cfg
			}
		}
		body["initial_data_source"] = map[string]any{"properties": title}
		resp, err = p.client.CreateDatabase(ctx, body)
		if err == nil {
			defer func() { p.addProperties(ctx, ds, sc, initial) }()
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", e.dir, err)
	}
	created, err := api.ParseObject(resp)
	if err != nil {
		return fmt.Errorf("%s: %w", e.dir, err)
	}
	e.newID = created.ID
	e.created = true
	p.databases[e.oldID] = created.ID
	p.blocks[e.oldID] = created.ID
	if e.parent != nil {
		e.parent.blockMap[e.oldID] = created.ID
	}
	if len(created.DataSources) == 0 {
		return fmt.Errorf("%s: created database has no data source", e.dir)
	}
	if ds != nil {
		ds.newID = created.DataSources[0].ID
		sc.newID = ds.newID
		p.registerDataSource(ctx, ds, sc)
	} else {
		e.placeholderDS = created.DataSources[0].ID
	}
	return nil
}

// adoptDataSource gives the bare data source a database was created
// with the schema of the first data source of the backup.
func (p *Exporter) adoptDataSource(ctx context.Context, e, db *entity) error {
	var raw map[string]any
	if err := json.Unmarshal(e.obj.Raw, &raw); err != nil {
		return err
	}
	sc := &schema{oldID: e.oldID, old: oldSchema(e.obj), newIDs: map[string]string{}, oldIDs: map[string]string{}, synced: map[string]bool{}}
	initial := phaseOne(sc.old)

	// the bare data source has a title property named Name
	props := map[string]any{}
	for name, cfg := range initial {
		if cfg.(map[string]any)["type"] == "title" {
			if name != "Name" {
				props["Name"] = map[string]any{"name": name}
			}
			continue
		}
		props[name] = cfg
	}
	body := map[string]any{
		"title":      p.richText(anyList(raw["title"])),
		"properties": props,
	}
	id := db.placeholderDS
	_, err := p.client.UpdateDataSource(ctx, id, body)
	if err != nil && api.IsValidation(err) {
		p.warnf("%s: schema not accepted, restoring it property by property: %v", e.dir, err)
		rename := map[string]any{}
		if r, ok := props["Name"]; ok {
			rename["Name"] = r
		}
		_, err = p.client.UpdateDataSource(ctx, id, map[string]any{"title": body["title"], "properties": rename})
		if err == nil {
			defer func() { p.addProperties(ctx, e, sc, initial) }()
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", e.dir, err)
	}
	db.placeholderDS = ""
	e.newID = id
	sc.newID = id
	p.registerDataSource(ctx, e, sc)
	return nil
}

// createDataSource creates a data source, or the database holding it
// when this is the first one.
func (p *Exporter) createDataSource(ctx context.Context, e *entity) error {
	db := e.parent
	if db == nil || db.kind != layout.EntityDatabase {
		return fmt.Errorf("%s: a data source must be under a database", e.dir)
	}
	if db.err != nil {
		return fmt.Errorf("%s: database: %w", e.dir, db.err)
	}
	if !db.created {
		if db.obj == nil {
			return fmt.Errorf("%s: database object missing", e.dir)
		}
		return p.createDatabase(ctx, db, e)
	}
	if db.placeholderDS != "" {
		return p.adoptDataSource(ctx, e, db)
	}

	var raw map[string]any
	if err := json.Unmarshal(e.obj.Raw, &raw); err != nil {
		return err
	}
	sc := &schema{oldID: e.oldID, old: oldSchema(e.obj), newIDs: map[string]string{}, oldIDs: map[string]string{}, synced: map[string]bool{}}
	initial := phaseOne(sc.old)
	body := map[string]any{
		"parent":     map[string]any{"type": "database_id", "database_id": db.newID},
		"title":      p.richText(anyList(raw["title"])),
		"properties": initial,
	}
	if icon, ok := raw["icon"].(map[string]any); ok && icon["type"] != "file" {
		if ref := iconRef(icon); ref != nil {
			body["icon"] = ref
		}
	}
	resp, err := p.client.CreateDataSource(ctx, body)
	if err != nil && api.IsValidation(err) {
		p.warnf("%s: schema not accepted, restoring it property by property: %v", e.dir, err)
		title := map[string]any{}
		for name, cfg := range initial {
			if cfg.(map[string]any)["type"] == "title" {
				title[name] = cfg
			}
		}
		body["properties"] = title
		resp, err = p.client.CreateDataSource(ctx, body)
		if err == nil {
			defer func() { p.addProperties(ctx, e, sc, initial) }()
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", e.dir, err)
	}
	created, err := api.ParseObject(resp)
	if err != nil {
		return fmt.Errorf("%s: %w", e.dir, err)
	}
	e.newID = created.ID
	sc.newID = created.ID
	p.registerDataSource(ctx, e, sc)
	return nil
}

// registerDataSource records the copy of a data source and queues the
// schema phases that need other objects.
func (p *Exporter) registerDataSource(ctx context.Context, e *entity, sc *schema) {
	p.dataSources[e.oldID] = e.newID
	p.schemas[e.newID] = sc
	for name, m := range sc.old {
		if id, ok := m["id"].(string); ok {
			sc.oldIDs[name] = id
		}
	}
	p.refreshIDs(ctx, sc)

	dir := e.dir
	p.relations = append(p.relations, func(ctx context.Context) error {
		return p.addRelations(ctx, dir, sc)
	})
	p.rollups = append(p.rollups, func(ctx context.Context) error {
		return p.addTyped(ctx, dir, sc, "rollup")
	})
	p.formulas = append(p.formulas, func(ctx context.Context) error {
		return p.addTyped(ctx, dir, sc, "formula")
	})
}

// refreshIDs reads the property ids of the copy.
func (p *Exporter) refreshIDs(ctx context.Context, sc *schema) {
	raw, err := p.client.DataSource(ctx, sc.newID)
	if err != nil {
		return
	}
	obj, err := api.ParseObject(raw)
	if err != nil {
		return
	}
	for name, pr := range obj.Properties {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(pr, &v) == nil {
			sc.newIDs[name] = v.ID
		}
	}
}

// addProperties adds properties one at a time, so that one the API
// refuses does not take the others down.
func (p *Exporter) addProperties(ctx context.Context, e *entity, sc *schema, props map[string]any) {
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cfg := props[name].(map[string]any)
		if cfg["type"] == "title" {
			continue
		}
		if _, err := p.client.UpdateDataSource(ctx, sc.newID, map[string]any{"properties": map[string]any{name: cfg}}); err != nil {
			if cfg["type"] == "status" {
				// options may be refused: a bare status still gets defaults
				if _, err2 := p.client.UpdateDataSource(ctx, sc.newID, map[string]any{"properties": map[string]any{name: map[string]any{"type": "status", "status": map[string]any{}}}}); err2 == nil {
					continue
				}
			}
			p.warnf("%s: property %q not restored: %v", e.dir, name, err)
		}
	}
	p.refreshIDs(ctx, sc)
}

// addRelations adds the relation properties, pointing at the copies of
// their targets, once every data source exists.
func (p *Exporter) addRelations(ctx context.Context, dir string, sc *schema) error {
	names := make([]string, 0)
	for name, m := range sc.old {
		if m["type"] == "relation" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if sc.synced[name] {
			continue
		}
		m := sc.old[name]
		rel, _ := m["relation"].(map[string]any)
		target, _ := rel["data_source_id"].(string)
		newTarget, ok := p.dataSources[target]
		if !ok {
			// a relation to a data source outside the restore would
			// let a snapshot add a property to it (dual relations
			// write to both sides): not ours to touch
			p.warnf("%s: relation %q: target data source %s not part of the restore", dir, name, target)
			continue
		}
		cfg := map[string]any{"data_source_id": newTarget}
		switch rel["type"] {
		case "dual_property":
			dual, _ := rel["dual_property"].(map[string]any)
			syncedName, _ := dual["synced_property_name"].(string)
			cfg["type"] = "dual_property"
			cfg["dual_property"] = map[string]any{}
			if syncedName != "" {
				cfg["dual_property"] = map[string]any{"synced_property_name": syncedName}
			}
			if tsc := p.schemas[newTarget]; tsc != nil && syncedName != "" {
				tsc.synced[syncedName] = true
			}
		default:
			cfg["type"] = "single_property"
			cfg["single_property"] = map[string]any{}
		}
		body := map[string]any{"properties": map[string]any{name: map[string]any{"type": "relation", "relation": cfg}}}
		if _, err := p.client.UpdateDataSource(ctx, sc.newID, body); err != nil {
			p.warnf("%s: relation %q not restored: %v", dir, name, err)
		}
	}
	p.refreshIDs(ctx, sc)
	return nil
}

// addTyped adds the properties of one type (rollups, then formulas).
func (p *Exporter) addTyped(ctx context.Context, dir string, sc *schema, typ string) error {
	names := make([]string, 0)
	for name, m := range sc.old {
		if m["type"] == typ {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		cfg := propertyConfig(name, sc.old[name])
		body := map[string]any{"properties": map[string]any{name: cfg}}
		if _, err := p.client.UpdateDataSource(ctx, sc.newID, body); err != nil {
			p.warnf("%s: %s %q not restored: %v", dir, typ, name, err)
		}
	}
	p.refreshIDs(ctx, sc)
	return nil
}

// --- row properties ---

var readOnlyValues = map[string]bool{
	"formula": true, "rollup": true, "created_time": true, "created_by": true,
	"last_edited_time": true, "last_edited_by": true, "unique_id": true, "button": true,
}

// rowProperties rewrites the properties of a row for creation.  Files
// wait for the assets, relations for every page to exist.
func (p *Exporter) rowProperties(ctx context.Context, e, ds *entity) (map[string]any, []string) {
	sc := p.schemas[ds.newID]
	props := map[string]any{}
	var dropped []string
	var relations []string

	for name, raw := range e.obj.Properties {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		typ, _ := m["type"].(string)
		if readOnlyValues[typ] {
			continue
		}
		if sc != nil {
			if om, ok := sc.old[name]; ok {
				if ot, _ := om["type"].(string); ot != typ {
					dropped = append(dropped, name)
					continue
				}
			}
		}
		v := m[typ]
		switch typ {
		case "title", "rich_text":
			rt := p.richText(anyList(v))
			if typ == "title" || len(rt) > 0 {
				props[name] = map[string]any{typ: rt}
			}
		case "number", "checkbox", "url", "email", "phone_number", "place":
			if v != nil {
				props[name] = map[string]any{typ: v}
			}
		case "select", "status":
			om, _ := v.(map[string]any)
			if n, ok := om["name"].(string); ok && n != "" {
				props[name] = map[string]any{typ: map[string]any{"name": n}}
			}
		case "multi_select":
			var opts []any
			for _, o := range anyList(v) {
				om, _ := o.(map[string]any)
				if n, ok := om["name"].(string); ok && n != "" {
					opts = append(opts, map[string]any{"name": n})
				}
			}
			if len(opts) > 0 {
				props[name] = map[string]any{typ: opts}
			}
		case "date":
			if dm, ok := v.(map[string]any); ok && dm["start"] != nil {
				d := map[string]any{"start": dm["start"]}
				if dm["end"] != nil {
					d["end"] = dm["end"]
				}
				if dm["time_zone"] != nil {
					d["time_zone"] = dm["time_zone"]
				}
				props[name] = map[string]any{typ: d}
			}
		case "people":
			var users []any
			for _, u := range anyList(v) {
				um, _ := u.(map[string]any)
				id, _ := um["id"].(string)
				if id != "" && p.knownUser(ctx, id) {
					users = append(users, map[string]any{"object": "user", "id": id})
				}
			}
			if len(users) > 0 {
				props[name] = map[string]any{typ: users}
			}
		case "files":
			var files []any
			for _, f := range anyList(v) {
				fm, _ := f.(map[string]any)
				if fm["type"] == "external" {
					ref, ok := p.fileRef(e, fm)
					if ok {
						if n, ok := fm["name"].(string); ok && n != "" {
							ref["name"] = n
						}
						files = append(files, ref)
					}
				} else {
					e.patch = true
				}
			}
			if len(files) > 0 {
				props[name] = map[string]any{typ: files}
			}
		case "verification":
			if vm, ok := v.(map[string]any); ok && vm["state"] != nil {
				props[name] = map[string]any{typ: map[string]any{"state": vm["state"]}}
			}
		case "relation":
			if len(anyList(v)) > 0 {
				relations = append(relations, name)
			}
		default:
			dropped = append(dropped, name)
		}
	}

	if len(relations) > 0 {
		props := e.obj.Properties
		dir := e.dir
		p.values = append(p.values, func(ctx context.Context) error {
			return p.setRelations(ctx, dir, e, props, relations)
		})
	}
	return props, dropped
}

// setRelations writes the relation values of a row once every page
// exists.  A relation the page object truncated is read back in full.
func (p *Exporter) setRelations(ctx context.Context, dir string, e *entity, props map[string]json.RawMessage, names []string) error {
	if e.newID == "" {
		return nil
	}
	body := map[string]any{}
	for _, name := range names {
		var m struct {
			ID       string `json:"id"`
			Relation []struct {
				ID string `json:"id"`
			} `json:"relation"`
			HasMore bool `json:"has_more"`
		}
		if err := json.Unmarshal(props[name], &m); err != nil {
			continue
		}
		var ids []string
		for _, r := range m.Relation {
			ids = append(ids, r.ID)
		}
		if m.HasMore {
			// the importer reads truncated values in full; a backup
			// that still carries has_more lost the rest
			p.warnf("%s: relation %q: the backup holds only the first %d pages", dir, name, len(ids))
		}
		var refs []any
		for _, id := range ids {
			if n, ok := p.pages[id]; ok {
				refs = append(refs, map[string]any{"id": n})
			}
		}
		if len(refs) == 0 {
			continue
		}
		if len(refs) > 100 {
			p.warnf("%s: relation %q: only the first 100 of %d pages restored", dir, name, len(refs))
			refs = refs[:100]
		}
		body[name] = map[string]any{"relation": refs}
	}
	if len(body) == 0 {
		return nil
	}
	if _, err := p.client.UpdatePage(ctx, e.newID, map[string]any{"properties": body}); err != nil {
		return fmt.Errorf("%s: relations not restored: %w", dir, err)
	}
	return nil
}
