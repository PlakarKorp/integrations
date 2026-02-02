/*
 * Copyright (c) 2025 Omar Polo <omar.polo@plakar.io>
 * Copyright (c) 2026 Gilles Chehade <gilles@plakar.io>
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
	"github.com/moby/moby/client"
)

const flags = location.FLAG_STREAM | location.FLAG_NEEDACK

func init() {
	importer.Register("docker+container", flags, NewImporter)
	importer.Register("docker+image", flags, NewImporter)
}

type Importer struct {
	ctx context.Context

	fp  io.ReadCloser
	tar *tar.Reader

	cleanup func() error

	location string
	name     string
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	imageName := strings.TrimPrefix(config["location"], name+"://")

	var fp io.ReadCloser
	var cleanup func() error
	if imageName == "" {
		fp = os.Stdin
	} else {
		var err error

		switch name {
		case "docker+container":
			fp, cleanup, err = dockerContainerSaveReader(ctx, imageName)
		case "docker+image":
			fp, cleanup, err = dockerImageSaveReader(ctx, imageName)
		}
		if err != nil {
			return nil, err
		}
	}

	t := &Importer{ctx: ctx, fp: fp, location: imageName, name: name, cleanup: cleanup}
	t.tar = tar.NewReader(fp)
	return t, nil
}

func (t *Importer) Type() string { return t.name }
func (t *Importer) Root() string { return "/" }
func (p *Importer) Origin() string {
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	return hostname
}

func (i *Importer) Flags() location.Flags {
	return flags
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
		Lusername:  "",
		Lgroupname: "",
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

func (t *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
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

func (t *Importer) Ping(ctx context.Context) error {
	return nil
}

func (t *Importer) Close(ctx context.Context) (err error) {
	t.fp.Close()
	if t.fp != nil {
		err = t.fp.Close()
	}

	return err
}

func dockerImageSaveReader(ctx context.Context, imageName string) (io.ReadCloser, func() error, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, nil, err
	}

	r, err := cli.ImageSave(ctx, []string{imageName})
	if err != nil {
		_ = cli.Close()
		return nil, nil, err
	}

	// cleanup function so you can close reader + client
	cleanup := func() error {
		_ = r.Close()
		return cli.Close()
	}

	return r, cleanup, nil
}

func dockerContainerSaveReader(ctx context.Context, imageName string) (io.ReadCloser, func() error, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, nil, err
	}

	r, err := cli.ContainerExport(ctx, imageName, client.ContainerExportOptions{})
	if err != nil {
		_ = cli.Close()
		return nil, nil, err
	}

	// cleanup function so you can close reader + client
	cleanup := func() error {
		_ = r.Close()
		return cli.Close()
	}

	return r, cleanup, nil
}
