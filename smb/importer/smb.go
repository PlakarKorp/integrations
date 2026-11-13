/*
 * Copyright (c) 2025 Gilles Chehade <gilles@poolp.org>
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

package importer

import (
	"context"
	"fmt"
	"io"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/exclude"
	"github.com/PlakarKorp/integration-sdk/location"
	plakarsmb "github.com/PlakarKorp/integrations/smb/common"
)

func init() {
	_ = integration.RegisterImporter("smb", 0, NewImporter)
}

type Importer struct {
	opts *integration.Options

	cfg  *plakarsmb.Config
	conn *plakarsmb.Conn

	rootDir  string
	excludes *exclude.RuleSet
}

func NewImporter(appCtx context.Context, opts *integration.Options, name string, config map[string]string) (integration.Importer, error) {
	cfg, err := plakarsmb.ParseConfig(config)
	if err != nil {
		return nil, err
	}

	excludes := exclude.NewRuleSet()
	if err := excludes.AddRulesFromArray(opts.Excludes); err != nil {
		return nil, fmt.Errorf("failed to setup exclude rules: %w", err)
	}

	conn, err := plakarsmb.Connect(appCtx, cfg)
	if err != nil {
		return nil, err
	}

	return &Importer{
		opts:     opts,
		cfg:      cfg,
		conn:     conn,
		rootDir:  cfg.Root,
		excludes: excludes,
	}, nil
}

func (imp *Importer) Type() string {
	return "smb"
}

func (imp *Importer) Origin() string {
	return imp.cfg.Origin()
}

func (imp *Importer) Root() string {
	return imp.rootDir
}

func (imp *Importer) Flags() location.Flags {
	return 0
}

func (imp *Importer) Import(ctx context.Context, records chan<- *integration.Record, results <-chan *integration.Result) (map[string]string, error) {
	defer close(records)
	return nil, imp.walk(ctx, records)
}

func (imp *Importer) Ping(ctx context.Context) error {
	_, err := imp.conn.Lstat(imp.rootDir)
	return err
}

func (imp *Importer) Close(ctx context.Context) error {
	return imp.conn.Close()
}

// open is the lazy reader handed to Plakar; pulling the bytes of a record opens
// the file on the share. go-smb2 multiplexes requests, so open handles are safe
// to read concurrently.
func (imp *Importer) open(p string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return imp.conn.Open(p)
	}
}
