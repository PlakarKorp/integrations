/*
 * Copyright (c) 2025 Gilles Chehade <gilles@plakar.io>
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
	"fmt"
	"io"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

type StdioExporter struct {
	filePath string
	appCtx   context.Context
	w        io.Writer
	opts     *connectors.Options
}

func init() {
	exporter.Register("stdout", 0, NewStdioExporter)
	exporter.Register("stderr", 0, NewStdioExporter)
}

func NewStdioExporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	var w io.Writer

	switch name {
	case "stdout":
		w = opts.Stdout
	case "stderr":
		w = opts.Stderr
	default:
		return nil, fmt.Errorf("unknown stdio backend %s", name)
	}

	return &StdioExporter{
		filePath: strings.TrimPrefix(config["location"], name+"://"),
		appCtx:   appCtx,
		w:        w,
		opts:     opts,
	}, nil
}

func (p *StdioExporter) Origin() string        { return p.opts.Hostname }
func (p *StdioExporter) Type() string          { return "stdio" }
func (p *StdioExporter) Root() string          { return "/" }
func (p *StdioExporter) Flags() location.Flags { return 0 }

func (p *StdioExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	for record := range records {
		if record.Err != nil || !record.FileInfo.Mode().IsRegular() {
			results <- record.Ok()
			continue
		}

		if _, err := io.Copy(p.w, record.Reader); err != nil {
			results <- record.Error(err)
		} else {
			results <- record.Ok()
		}
	}
	return nil
}

func (p *StdioExporter) Ping(ctx context.Context) error {
	return nil
}

func (p *StdioExporter) Close(ctx context.Context) error {
	return nil
}
