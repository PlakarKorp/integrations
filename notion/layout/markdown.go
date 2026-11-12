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

package layout

import (
	"regexp"
	"strings"

	"github.com/PlakarKorp/integrations/notion/api"
)

var (
	pageTagRe     = regexp.MustCompile(`<page\s+[^>]*?url="([^"]*)"`)
	databaseTagRe = regexp.MustCompile(`<database\s+[^>]*>`)
	attrURLRe     = regexp.MustCompile(`\burl="([^"]*)"`)
	attrDSURLRe   = regexp.MustCompile(`\bdata-source-url="([^"]*)"`)
	urlRe         = regexp.MustCompile(`https?://[^\s<>"'()\]]+`)
)

// DatabaseLink is a child database as markdown references it.
type DatabaseLink struct {
	ID           string
	DataSourceID string
}

// Scan lists what a page's markdown links to: its child pages, its
// child databases and the Notion-hosted files it embeds.
func Scan(md string) (pages []string, databases []DatabaseLink, files []string) {
	seen := map[string]struct{}{}
	for _, m := range pageTagRe.FindAllStringSubmatch(md, -1) {
		if id := api.IDFromURL(html(m[1])); id != "" {
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				pages = append(pages, id)
			}
		}
	}
	for _, tag := range databaseTagRe.FindAllString(md, -1) {
		var link DatabaseLink
		if m := attrURLRe.FindStringSubmatch(tag); m != nil {
			link.ID = api.IDFromURL(html(m[1]))
		}
		if m := attrDSURLRe.FindStringSubmatch(tag); m != nil {
			link.DataSourceID = api.IDFromURL(html(m[1]))
		}
		if link.ID == "" && link.DataSourceID == "" {
			continue
		}
		key := link.ID + "/" + link.DataSourceID
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		databases = append(databases, link)
	}
	for _, u := range urlRe.FindAllString(md, -1) {
		u = html(u)
		if !api.IsNotionFileURL(u) {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		files = append(files, u)
	}
	return pages, databases, files
}

// Rewrite replaces the signed urls of Notion-hosted files with the
// relative path of the asset in the snapshot, so that the markdown
// stays readable long after the urls expired.
func Rewrite(md string, resolve func(url string) (string, bool)) string {
	return urlRe.ReplaceAllStringFunc(md, func(u string) string {
		if !api.IsNotionFileURL(html(u)) {
			return u
		}
		if rel, ok := resolve(html(u)); ok {
			return rel
		}
		return u
	})
}

func html(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	r := strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">")
	return r.Replace(s)
}
