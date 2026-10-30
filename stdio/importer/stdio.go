/*
 * Copyright (c) 2023 Gilles Chehade <gilles@poolp.org>
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
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
	"github.com/PlakarKorp/kloset/connectors/importer"
)

type StdioImporter struct {
	stdin   io.Reader
	fileDir string
	ctx     context.Context
	opts    *integration.Options
	name    string
}

func init() {
	importer.Register("stdin", 0, NewStdioImporter)
}

func NewStdioImporter(ctx context.Context, opts *integration.Options, name string, config map[string]string) (importer.Importer, error) {
	location := config["location"]
	location = strings.TrimPrefix(location, "stdin://")
	if !strings.HasPrefix(location, "/") {
		location = "/" + location
	}
	location = path.Clean(location)

	return &StdioImporter{
		stdin:   opts.Stdin,
		fileDir: location,
		ctx:     ctx,
		name:    name,
		opts:    opts,
	}, nil
}

func (p *StdioImporter) stdioWalker_addPrefixDirectories(results chan<- *integration.Record) {
	directory := path.Clean(p.fileDir)
	atoms := strings.Split(directory, string(os.PathSeparator))

	for i := 0; i < len(atoms)-1; i++ {
		subpath := path.Join(atoms[0 : i+1]...)

		if !strings.HasPrefix(subpath, "/") {
			subpath = "/" + subpath
		}

		fi := integration.FileInfo{
			Lname:      path.Base(subpath),
			Lmode:      0755 | os.ModeDir,
			Lsize:      0,
			Ldev:       0,
			Lino:       0,
			Luid:       0,
			Lgid:       0,
			Lnlink:     0,
			LmodTime:   time.Now(),
			Lusername:  "",
			Lgroupname: "",
		}
		results <- integration.NewRecord(subpath, "", fi, nil, nil)
	}
}

func (p *StdioImporter) Import(ctx context.Context, records chan<- *integration.Record, results <-chan *integration.Result) error {
	defer close(records)
	p.stdioWalker_addPrefixDirectories(records)
	fi := integration.FileInfo{
		Lname:      path.Base(p.fileDir),
		Lmode:      0644,
		Lsize:      -1,
		Ldev:       0,
		Lino:       0,
		Luid:       0,
		Lgid:       0,
		Lnlink:     0,
		LmodTime:   time.Now(),
		Lusername:  "",
		Lgroupname: "",
	}
	records <- integration.NewRecord(p.fileDir, "", fi, nil,
		func() (io.ReadCloser, error) { return io.NopCloser(p.stdin), nil })

	return nil
}

func (p *StdioImporter) Ping(ctx context.Context) error {
	return nil
}

func (p *StdioImporter) Close(ctx context.Context) error {
	return nil
}

func (p *StdioImporter) Root() string          { return "/" }
func (p *StdioImporter) Origin() string        { return p.opts.Hostname }
func (p *StdioImporter) Type() string          { return p.name }
func (p *StdioImporter) Flags() location.Flags { return 0 }
