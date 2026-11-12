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

// Package importer backs up a Notion workspace.
//
// Everything the token can see is listed through search, then walked
// from the top: a page costs one request for its markdown, which tells
// its children and the files it embeds, plus one per block for the
// comments, since discussions live on blocks.  With comments set to
// pages or none, the block tree, the files and the markdown are only
// read when kloset asks for them, which it does not for a page whose
// last_edited_time has not moved since the previous snapshot.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/layout"
	"github.com/PlakarKorp/kloset/exclude"
)

const (
	CommentsNone   = "none"
	CommentsPages  = "pages"
	CommentsBlocks = "blocks"

	// recentEdit is how long after an edit we still consider a page
	// changed, because last_edited_time has a minute granularity and
	// an edit made during the previous backup's minute would be missed.
	recentEdit = 2 * time.Minute

	maxConcurrency = 8
)

var epoch = time.Unix(0, 0).UTC()

// Config is what to back up and how.
type Config struct {
	Root     string // object id to back up, "" for the whole workspace
	Comments string // none, pages or blocks
	Users    bool
	Views    bool
}

// Importer backs a workspace up.
type Importer struct {
	opts     *integration.Options
	client   *api.Client
	cfg      Config
	excludes *exclude.RuleSet
	origin   string
	me       json.RawMessage

	// readers may run after Import returned and its context died
	bg     context.Context
	cancel context.CancelFunc

	started time.Time
	conc    int
}

// ParseLocation accepts notion://, notion://<id> and notion:///<id>.
func ParseLocation(name, loc string) (string, error) {
	rest := strings.TrimPrefix(loc, name+"://")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", nil
	}
	if id := api.IDFromURL("https://www.notion.so/" + rest); id != "" {
		return id, nil
	}
	if api.IsID(rest) {
		return api.ExpandID(rest), nil
	}
	return "", fmt.Errorf("invalid notion location %q: expected notion:// or notion://<object id>", loc)
}

func token(config map[string]string) string {
	if t := config["token"]; t != "" {
		return t
	}
	return os.Getenv("NOTION_TOKEN")
}

func boolOpt(config map[string]string, key string, def bool) (bool, error) {
	v, ok := config[key]
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("invalid value for %s: %w", key, err)
	}
	return b, nil
}

// NewImporter is the plugin entry point: it reads the configuration
// plakar passes and builds the importer.
func NewImporter(ctx context.Context, opts *integration.Options, name string, config map[string]string) (integration.Importer, error) {
	root, err := ParseLocation(name, config["location"])
	if err != nil {
		return nil, err
	}
	cfg := Config{Root: root, Comments: CommentsBlocks}
	if v := config["comments"]; v != "" {
		switch v {
		case CommentsNone, CommentsPages, CommentsBlocks:
			cfg.Comments = v
		default:
			return nil, fmt.Errorf("invalid value for comments: %q (none, pages or blocks)", v)
		}
	}
	if cfg.Users, err = boolOpt(config, "users", true); err != nil {
		return nil, err
	}
	if cfg.Views, err = boolOpt(config, "views", true); err != nil {
		return nil, err
	}
	rate := 0.0
	if v := config["rate"]; v != "" {
		if rate, err = strconv.ParseFloat(v, 64); err != nil || rate <= 0 {
			return nil, fmt.Errorf("invalid value for rate: %q (requests per second)", v)
		}
	}
	client, err := api.New(api.Config{
		Token:       token(config),
		BaseURL:     config["api_url"],
		Rate:        rate,
		Concurrency: min(max(opts.MaxConcurrency, 1), maxConcurrency),
	})
	if err != nil {
		return nil, err
	}
	excludes := exclude.NewRuleSet()
	if err := excludes.AddRulesFromArray(opts.Excludes); err != nil {
		return nil, fmt.Errorf("exclude rules: %w", err)
	}
	return New(ctx, opts, client, cfg, excludes)
}

// New builds an importer on a client; it fetches the bot user, which
// both checks the token and names the origin.
func New(ctx context.Context, opts *integration.Options, client *api.Client, cfg Config, excludes *exclude.RuleSet) (*Importer, error) {
	if excludes == nil {
		excludes = exclude.NewRuleSet()
	}
	if cfg.Comments == "" {
		cfg.Comments = CommentsBlocks
	}
	me, err := client.Me(ctx)
	if err != nil {
		return nil, fmt.Errorf("notion: %w", err)
	}
	var bot struct {
		Name string `json:"name"`
		Bot  struct {
			WorkspaceName string `json:"workspace_name"`
		} `json:"bot"`
	}
	_ = json.Unmarshal(me, &bot)
	origin := bot.Bot.WorkspaceName
	if origin == "" {
		origin = "notion"
	}
	bg, cancel := context.WithCancel(context.Background())
	conc := 1
	if opts != nil {
		conc = min(max(opts.MaxConcurrency, 1), maxConcurrency)
	}
	return &Importer{
		opts:     opts,
		client:   client,
		cfg:      cfg,
		excludes: excludes,
		origin:   origin,
		me:       me,
		bg:       bg,
		cancel:   cancel,
		conc:     conc,
	}, nil
}

func (p *Importer) Origin() string        { return p.origin }
func (p *Importer) Type() string          { return "notion" }
func (p *Importer) Root() string          { return "/" }
func (p *Importer) Flags() location.Flags { return 0 }
func (p *Importer) Close(context.Context) error {
	p.cancel()
	return nil
}

func (p *Importer) Ping(ctx context.Context) error {
	_, err := p.client.Me(ctx)
	return err
}

func (p *Importer) Import(ctx context.Context, records chan<- *integration.Record, _ <-chan *integration.Result) error {
	defer close(records)
	p.started = time.Now()

	em := &emitter{records: records, excludes: p.excludes}
	em.dir("/", epoch)

	em.file("/"+layout.WorkspaceFile, p.workspaceFile(), p.started)

	if p.cfg.Users {
		if err := p.emitUsers(ctx, em); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			em.err("/"+layout.UsersFile, err)
		}
	}

	idx, err := p.buildIndex(ctx)
	if err != nil {
		return err
	}

	w := &walker{imp: p, idx: idx, em: em}
	if err := w.run(ctx); err != nil {
		return err
	}
	return nil
}

func (p *Importer) workspaceFile() []byte {
	out := map[string]any{
		"api_version": api.Version,
		"backup_time": p.started.UTC().Format(time.RFC3339),
		"root":        p.cfg.Root,
		"bot":         json.RawMessage(p.me),
	}
	raw, _ := json.MarshalIndent(out, "", "  ")
	return raw
}

func (p *Importer) emitUsers(ctx context.Context, em *emitter) error {
	var users []json.RawMessage
	err := p.client.Users(ctx, func(raw json.RawMessage) error {
		users = append(users, raw)
		return nil
	})
	if err != nil {
		if api.IsForbidden(err) {
			// no user information capability: not an error worth a record
			return nil
		}
		return err
	}
	raw, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	em.file("/"+layout.UsersFile, raw, p.started)
	return nil
}

// emitter writes records, applying the exclude rules, from several
// walkers at once.
type emitter struct {
	records  chan<- *integration.Record
	excludes *exclude.RuleSet

	mu   sync.Mutex
	dirs map[string]struct{}
}

func (em *emitter) excluded(p string, isDir bool) bool {
	return em.excludes != nil && em.excludes.IsExcluded(p, isDir)
}

// dir emits a directory once.
func (em *emitter) dir(p string, mtime time.Time) bool {
	if em.excluded(p, true) {
		return false
	}
	em.mu.Lock()
	if em.dirs == nil {
		em.dirs = map[string]struct{}{}
	}
	_, seen := em.dirs[p]
	em.dirs[p] = struct{}{}
	em.mu.Unlock()
	if seen {
		return true
	}
	info := integration.NewFileInfo(path.Base(p), 0, os.ModeDir|0o755, mtime, 0, 0, 0, 0, 1)
	if p == "/" {
		info.Lname = "/"
	}
	em.records <- integration.NewRecord(p, "", info, nil, nil)
	return true
}

func (em *emitter) file(p string, data []byte, mtime time.Time) {
	if em.excluded(p, false) {
		return
	}
	info := integration.NewFileInfo(path.Base(p), int64(len(data)), 0o644, mtime, 0, 0, 0, 0, 1)
	em.records <- integration.NewRecord(p, "", info, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})
}

// lazy emits a file whose content is only produced when read, with an
// unknown size so that kloset trusts the modification time alone.
func (em *emitter) lazy(p string, mtime time.Time, open func() (io.ReadCloser, error)) {
	if em.excluded(p, false) {
		return
	}
	info := integration.NewFileInfo(path.Base(p), -1, 0o644, mtime, 0, 0, 0, 0, 1)
	em.records <- integration.NewRecord(p, "", info, nil, open)
}

func (em *emitter) err(p string, err error) {
	em.records <- integration.NewError(p, err)
}

// mtime is the modification time kloset compares against the previous
// snapshot: the object's last edit, unless it is so recent that an
// edit in the same minute could still follow.
func (p *Importer) mtime(lastEdited time.Time) time.Time {
	if lastEdited.IsZero() {
		return p.started
	}
	if p.started.Sub(lastEdited) < recentEdit {
		return p.started
	}
	return lastEdited.UTC()
}

// --- markdown ---

// markdown fetches a page's markdown and inlines the subtrees the
// server left out of an oversized page.  An unknown block that is a
// page or a database of its own gets the tag naming it (resolve
// returns it), the others are fetched; what the token cannot see
// keeps its <unknown> tag.
func (p *Importer) markdown(ctx context.Context, id string, resolve func(string) (string, bool)) (string, error) {
	md, err := p.client.PageMarkdown(ctx, id)
	if err != nil {
		return "", err
	}
	text := md.Markdown
	pending := append([]string(nil), md.UnknownBlockIDs...)
	seen := map[string]struct{}{id: {}}
	for budget := 200; len(pending) > 0 && budget > 0; budget-- {
		bid := pending[0]
		pending = pending[1:]
		if _, dup := seen[bid]; dup {
			continue
		}
		seen[bid] = struct{}{}
		if resolve != nil {
			if tag, ok := resolve(bid); ok {
				text = spliceUnknown(text, bid, tag)
				continue
			}
		}
		sub, err := p.client.PageMarkdown(ctx, bid)
		if err != nil {
			if api.IsNotFound(err) || api.IsForbidden(err) || api.IsValidation(err) {
				continue
			}
			return "", err
		}
		text = spliceUnknown(text, bid, sub.Markdown)
		pending = append(pending, sub.UnknownBlockIDs...)
	}
	return text, nil
}

// spliceUnknown replaces the <unknown .../> tag naming a block with the
// markdown fetched for it, or appends it when the tag is not found.
func spliceUnknown(text, blockID, sub string) string {
	compact := api.CompactID(blockID)
	start := 0
	for {
		i := strings.Index(text[start:], "<unknown")
		if i < 0 {
			break
		}
		i += start
		end := strings.Index(text[i:], ">")
		if end < 0 {
			break
		}
		tag := text[i : i+end+1]
		if strings.Contains(api.CompactID(tag), compact) {
			return text[:i] + strings.TrimSpace(sub) + text[i+end+1:]
		}
		start = i + end + 1
	}
	return text + "\n\n" + strings.TrimSpace(sub) + "\n"
}

// --- blocks ---

var noDescent = map[string]bool{"child_page": true, "child_database": true}

// maxBlockDepth bounds the recursion into block children; Notion
// itself nests far less deep.
const maxBlockDepth = 64

// blockTree fetches the complete block tree under a page or block.
// Each block is the object the API returned plus a "children" array.
func (p *Importer) blockTree(ctx context.Context, id string) ([]map[string]any, error) {
	return p.blockSubtree(ctx, id, map[string]struct{}{id: {}}, 0)
}

func (p *Importer) blockSubtree(ctx context.Context, id string, seen map[string]struct{}, depth int) ([]map[string]any, error) {
	if depth > maxBlockDepth {
		return nil, fmt.Errorf("block %s: nested deeper than %d levels", id, maxBlockDepth)
	}
	var out []map[string]any
	err := p.client.BlockChildren(ctx, id, func(raw json.RawMessage) error {
		var b map[string]any
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("decoding block: %w", err)
		}
		out = append(out, b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, b := range out {
		has, _ := b["has_children"].(bool)
		typ, _ := b["type"].(string)
		bid, _ := b["id"].(string)
		if !has || noDescent[typ] || bid == "" {
			continue
		}
		if _, dup := seen[bid]; dup {
			b["children_error"] = "block already seen higher in the tree"
			continue
		}
		seen[bid] = struct{}{}
		children, err := p.blockSubtree(ctx, bid, seen, depth+1)
		if err != nil {
			if api.IsNotFound(err) || api.IsForbidden(err) {
				b["children_error"] = err.Error()
				continue
			}
			return nil, err
		}
		b["children"] = children
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, nil
}

func (p *Importer) blocksJSON(ctx context.Context, id string) ([]byte, error) {
	tree, err := p.blockTree(ctx, id)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(tree, "", "  ")
}

// walkTree calls fn for every block of a tree, parents first.
func walkTree(tree []map[string]any, fn func(b map[string]any)) {
	for _, b := range tree {
		fn(b)
		if c, ok := b["children"].([]map[string]any); ok {
			walkTree(c, fn)
		}
	}
}

// --- comments ---

func (p *Importer) comments(ctx context.Context, id string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	err := p.client.Comments(ctx, id, func(raw json.RawMessage) error {
		out = append(out, raw)
		return nil
	})
	if err != nil {
		if api.IsForbidden(err) {
			// the token has no comment capability
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

// --- assets ---

// asset is a Notion-hosted file to back up under an object.
type asset struct {
	key string
	url string
}

// freshURL finds a new signed url for an asset of an object, once the
// one found at scan time has expired: the page object, its markdown
// and its comments are looked at in turn.
func (p *Importer) freshURL(ctx context.Context, e *entry, key string) (string, error) {
	var raw json.RawMessage
	var err error
	switch e.kind {
	case layout.EntityDatabase:
		raw, err = p.client.Database(ctx, e.obj.ID)
	default:
		raw, err = p.client.Page(ctx, e.obj.ID)
	}
	if err != nil {
		return "", err
	}
	for _, ref := range api.FileRefs(raw) {
		if ref.Key == key {
			return ref.URL, nil
		}
	}
	if e.kind == layout.EntityPage {
		md, err := p.markdown(ctx, e.obj.ID, nil)
		if err == nil {
			_, _, files := layout.Scan(md)
			for _, u := range files {
				if k, ok := api.AssetKey(u); ok && k == key {
					return u, nil
				}
			}
		}
		if p.cfg.Comments != CommentsNone {
			comments, err := p.comments(ctx, e.obj.ID)
			if err == nil {
				for _, c := range comments {
					for _, ref := range api.FileRefs(c) {
						if ref.Key == key {
							return ref.URL, nil
						}
					}
				}
			}
		}
	}
	return "", fmt.Errorf("asset %s no longer referenced by %s", key, e.obj.ID)
}

// openAsset downloads an asset, refreshing its url when it expired.
func (p *Importer) openAsset(e *entry, a asset) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		ctx := p.bg
		rc, _, err := p.client.Download(ctx, a.url)
		if errors.Is(err, api.ErrExpiredURL) {
			u, ferr := p.freshURL(ctx, e, a.key)
			if ferr != nil {
				return nil, fmt.Errorf("%s: %w", a.key, ferr)
			}
			rc, _, err = p.client.Download(ctx, u)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.key, err)
		}
		return rc, nil
	}
}
