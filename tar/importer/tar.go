/*
 * Copyright (c) 2025 Omar Polo <omar.polo@plakar.io>
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
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

const flags = location.FLAG_LOCALFS | location.FLAG_STREAM | location.FLAG_NEEDACK

func init() {
	importer.Register("tar", flags, NewTarImporter)
	importer.Register("tar+gz", flags, NewTarImporter)
	importer.Register("tar+gzip", flags, NewTarImporter)
	importer.Register("tgz", flags, NewTarImporter)
}

type TarImporter struct {
	ctx context.Context

	fp  *os.File
	rd  *gzip.Reader
	tar *tar.Reader

	opts *connectors.Options

	location string
	name     string
}

func NewTarImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	location := strings.TrimPrefix(config["location"], name+"://")

	fp, err := os.Open(location)
	if err != nil {
		return nil, err
	}

	t := &TarImporter{ctx: ctx, fp: fp, location: location, name: name, opts: opts}

	if name == "tar+gz" || name == "tar+gzip" || name == "tgz" {
		rd, err := gzip.NewReader(fp)
		if err != nil {
			t.Close(ctx)
			return nil, err
		}
		t.rd = rd
		t.tar = tar.NewReader(t.rd)
	} else {
		t.tar = tar.NewReader(t.fp)
	}

	return t, nil
}

func (t *TarImporter) Type() string          { return t.name }
func (t *TarImporter) Root() string          { return "/" }
func (t *TarImporter) Origin() string        { return t.opts.Hostname }
func (t *TarImporter) Flags() location.Flags { return flags }

func (t *TarImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	for {
		hdr, err := t.tar.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}

		name := path.Join("/", hdr.Name)
		records <- &connectors.Record{
			Pathname: name,
			Target:   hdr.Linkname,
			FileInfo: finfo(hdr),
			Reader:   io.NopCloser(t.tar),
		}

		select {
		case <-t.ctx.Done():
			return t.ctx.Err()

		case <-results:
			// wait for the ack before continuing
		}
	}

}

func finfo(hdr *tar.Header) objects.FileInfo {
	f := objects.FileInfo{
		Lname:      path.Base(hdr.Name),
		Lsize:      hdr.Size,
		Lmode:      fs.FileMode(hdr.Mode),
		LmodTime:   hdr.ModTime,
		Ldev:       0, // XXX could use hdr.Devminor / hdr.Devmajor
		Luid:       uint64(hdr.Uid),
		Lgid:       uint64(hdr.Gid),
		Lnlink:     1,
		Lusername:  hdr.Uname,
		Lgroupname: hdr.Gname,
	}

	switch hdr.Typeflag {
	case tar.TypeLink:
		f.Lmode |= fs.ModeSymlink
	case tar.TypeChar:
		f.Lmode |= fs.ModeCharDevice
	case tar.TypeBlock:
		f.Lmode |= fs.ModeDevice
	case tar.TypeDir:
		f.Lmode |= fs.ModeDir
	case tar.TypeFifo:
		f.Lmode |= fs.ModeNamedPipe
	default:
		// other are implicitly regular files.
	}

	return f
}

func (t *TarImporter) Ping(ctx context.Context) error {
	return nil
}

func (t *TarImporter) Close(ctx context.Context) (err error) {
	t.fp.Close()
	if t.rd != nil {
		err = t.rd.Close()
	}

	return err
}
