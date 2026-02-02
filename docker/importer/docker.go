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
	"time"

	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/importer"
	"github.com/moby/moby/client"
)

func init() {
	importer.Register("docker", location.FLAG_STREAM, NewDockerImporter)
}

type DockerImporter struct {
	ctx context.Context

	fp  io.ReadCloser
	tar *tar.Reader

	cleanup func() error

	location string
	name     string

	next chan struct{}
}

func NewDockerImporter(ctx context.Context, opts *importer.Options, name string, config map[string]string) (importer.Importer, error) {
	imageName := strings.TrimPrefix(config["location"], name+"://")

	var fp io.ReadCloser
	var cleanup func() error
	if imageName == "" {
		fp = os.Stdin
	} else {
		var err error
		fp, cleanup, err = dockerContainerSaveReader(ctx, imageName)
		if err != nil {
			return nil, err
		}
	}

	t := &DockerImporter{ctx: ctx, fp: fp, location: imageName, name: name, cleanup: cleanup}
	t.tar = tar.NewReader(t.fp)
	t.next = make(chan struct{}, 1)

	return t, nil
}

func (t *DockerImporter) Type(ctx context.Context) (string, error) { return t.name, nil }
func (t *DockerImporter) Root(ctx context.Context) (string, error) { return "/", nil }

func (p *DockerImporter) Origin(ctx context.Context) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	return hostname, nil
}

func (t *DockerImporter) Scan(ctx context.Context) (<-chan *importer.ScanResult, error) {
	ch := make(chan *importer.ScanResult, 1)
	go t.scan(ch)
	return ch, nil
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

type entry struct {
	t  *tar.Reader
	ch chan<- struct{}
}

func (e *entry) Read(buf []byte) (int, error) {
	return e.t.Read(buf)
}

func (e *entry) Close() error {
	e.ch <- struct{}{}
	return nil
}

func (t *DockerImporter) scan(ch chan<- *importer.ScanResult) {
	defer close(ch)

	info := objects.NewFileInfo("/", 0, 0700|os.ModeDir, time.Unix(0, 0), 0, 0, 0, 0, 1)
	ch <- &importer.ScanResult{
		Record: &importer.ScanRecord{
			Pathname: "/",
			FileInfo: info,
		},
	}

	for {
		hdr, err := t.tar.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				ch <- importer.NewScanError(t.location, err)
			}
			return
		}

		name := path.Join("/", hdr.Name)
		ch <- &importer.ScanResult{
			Record: &importer.ScanRecord{
				Pathname: name,
				Target:   hdr.Linkname,
				FileInfo: finfo(hdr),
				Reader:   &entry{t.tar, t.next},
			},
		}

		select {
		case <-t.next:
		case <-t.ctx.Done():
			return
		}
	}
}

func (t *DockerImporter) Close(ctx context.Context) (err error) {
	if t.fp != nil && t.fp != os.Stdin {
		_ = t.fp.Close()
	}

	if t.cleanup != nil {
		t.cleanup()
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
