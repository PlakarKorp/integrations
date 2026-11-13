/*
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

// Package docker backs up and restores what a docker daemon holds: images,
// networks, volumes (configuration and data) and containers
// (configuration).  The snapshot is laid out as
//
//	/manifest.json
//	/images/<repo>/<tag>.tar
//	/networks/<name>.json
//	/volumes/<name>/config.json
//	/volumes/<name>/data/...
//	/containers/<name>/config.json
//	/compose/<project>/compose.yaml
//	/compose/<project>/project.json
//	/compose/<project>/files/<path>
//
// docker+compose:/<project> walks the objects carrying the project's
// compose labels and adds the rendered configuration under /compose; the
// restore to docker+compose:/<dir> writes it there and runs compose up.
//
// Volume data goes through a helper container running the same image the
// k8s integration uses for PVCs: plakar's fs importer or exporter behind an
// mTLS gRPC listener, with the volume mounted at /data.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/moby/moby/client"
)

const (
	// the image k8s runs for PVCs; it carries the fs importer and exporter.
	defaultKubeletImage = "ghcr.io/plakarkorp/kubelet:2f6a9ac0f797b71279cc89daeff4966c66e5d169-35656206139"

	// label set on the helper containers so the walk leaves them out.
	helperLabel = "io.plakar.docker.helper"

	manifestVersion = 1
)

// the walk cannot know the size of an image save stream; kloset accepts
// -1 and fills it in once the content has been read.
const unknownSize = -1

func init() {
	_ = importer.Register("docker", location.FLAG_STREAM|location.FLAG_NEEDACK, NewImporter)
	_ = importer.Register("docker+compose", location.FLAG_STREAM|location.FLAG_NEEDACK, NewImporter)
	_ = exporter.Register("docker", location.FLAG_WALKDIRORDER, NewExporter)
	_ = exporter.Register("docker+compose", location.FLAG_WALKDIRORDER, NewExporter)
}

type Manifest struct {
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	Hostname      string    `json:"hostname"`
	ServerVersion string    `json:"server_version"`
	APIVersion    string    `json:"api_version"`
	Platform      string    `json:"platform"`
	OS            string    `json:"os"`
	Architecture  string    `json:"architecture"`

	ComposeVersion string `json:"compose_version,omitempty"`
}

type Docker struct {
	cli   *client.Client
	opts  *connectors.Options
	proto string
	host  string

	// what the location narrows the walk to: kind is one of "",
	// "images", "networks", "volumes", "containers"; name is the rest of
	// the path, so "postgres/17" for an image.
	kind, name string

	// restore side: where the restored subtree lands in the layout, so
	// that "-to docker:/volumes/foo <snap>:/volumes/foo" works the way
	// "-to /tmp/foo <snap>:/some/foo" does for a filesystem.
	base string

	pause        bool
	start        bool
	kubeletImage string

	export bool

	// docker+compose: the project walked, what the labels say about it,
	// the configuration compose rendered and the images it names.
	project  string
	proj     Project
	rendered []byte
	images   map[string]bool

	// docker+compose restore: the directory the configuration lands in,
	// and the compose.yaml once written.
	dir         string
	composeFile string

	// backup side: the helpers serving volume data, stopped at the end.
	helpers []*helper

	// restore side: the helper feeding the volume being restored, if any.
	cur *restoreHelper
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	return newDocker(ctx, opts, name, config, false)
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	return newDocker(ctx, opts, name, config, true)
}

func newDocker(ctx context.Context, opts *connectors.Options, proto string, config map[string]string, export bool) (*Docker, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	d := &Docker{
		cli:          cli,
		opts:         opts,
		proto:        proto,
		export:       export,
		kubeletImage: defaultKubeletImage,
	}

	rest := strings.TrimPrefix(config["location"], proto+":")
	rest = strings.TrimPrefix(rest, "//")
	p := path.Clean("/" + rest)
	d.base = p
	switch {
	case proto == "docker+compose" && export:
		d.base = "/"
		if rest == "" {
			cli.Close()
			return nil, fmt.Errorf("%s: a directory to restore the project into is needed", config["location"])
		}
		if d.dir, err = filepath.Abs(rest); err != nil {
			cli.Close()
			return nil, err
		}
	case proto == "docker+compose":
		d.project = strings.Trim(p, "/")
		if d.project == "" || strings.Contains(d.project, "/") {
			cli.Close()
			return nil, fmt.Errorf("%s: want docker+compose:/<project>", config["location"])
		}
	case p != "/":
		segs := strings.SplitN(strings.Trim(p, "/"), "/", 2)
		switch segs[0] {
		case "images", "networks", "volumes", "containers":
			d.kind = segs[0]
		default:
			cli.Close()
			return nil, fmt.Errorf("unknown kind %q in %q: want images, networks, volumes or containers", segs[0], config["location"])
		}
		if len(segs) == 2 {
			d.name = segs[1]
		}
	}

	if v := config["pause"]; v != "" {
		if d.pause, err = strconv.ParseBool(v); err != nil {
			cli.Close()
			return nil, fmt.Errorf("pause: %w", err)
		}
	}
	if v := config["start"]; v != "" {
		if d.start, err = strconv.ParseBool(v); err != nil {
			cli.Close()
			return nil, fmt.Errorf("start: %w", err)
		}
	}
	if v := config["kubelet_image"]; v != "" {
		d.kubeletImage = v
	}

	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("docker info: %w", err)
	}
	d.host = info.Info.Name

	return d, nil
}

func (d *Docker) Origin() string { return d.host }
func (d *Docker) Type() string   { return d.proto }
func (d *Docker) Root() string   { return "/" }

func (d *Docker) Flags() location.Flags {
	if d.export {
		return location.FLAG_WALKDIRORDER
	}
	return location.FLAG_STREAM | location.FLAG_NEEDACK
}

func (d *Docker) Ping(ctx context.Context) error {
	_, err := d.cli.Ping(ctx, client.PingOptions{})
	return err
}

func (d *Docker) Close(ctx context.Context) error {
	var err error
	d.stopHelpers(ctx)
	if d.cur != nil {
		err = d.cur.finish(ctx)
		d.cur = nil
	}
	if cerr := d.cli.Close(); err == nil {
		err = cerr
	}
	return err
}

// wants reports whether the walk should visit kind, and if name is given,
// that object in particular.
func (d *Docker) wants(kind, name string) bool {
	if d.kind == "" {
		return true
	}
	if d.kind != kind {
		return false
	}
	if name == "" {
		// asking whether the kind as a whole is wanted
		return true
	}
	return d.name == "" || d.name == name
}

func fileRecord(pathname string, data []byte, mtime time.Time) *connectors.Record {
	finfo := objects.FileInfo{
		Lname:    path.Base(pathname),
		Lsize:    int64(len(data)),
		Lmode:    0644,
		LmodTime: mtime,
	}
	return connectors.NewRecord(pathname, "", finfo, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})
}

func streamRecord(pathname string, mtime time.Time, open func() (io.ReadCloser, error)) *connectors.Record {
	finfo := objects.FileInfo{
		Lname:    path.Base(pathname),
		Lsize:    unknownSize,
		Lmode:    0644,
		LmodTime: mtime,
	}
	return connectors.NewRecord(pathname, "", finfo, nil, open)
}

// splitRef splits an image reference into repository and tag, so
// "ghcr.io/plakarkorp/kubelet:abc" gives ("ghcr.io/plakarkorp/kubelet", "abc").
func splitRef(ref string) (repo, tag string) {
	i := strings.LastIndex(ref, ":")
	if i < 0 || i < strings.LastIndex(ref, "/") {
		return ref, "latest"
	}
	return ref[:i], ref[i+1:]
}

// hostFileRecord is a record for a file on the host plakar runs on, opened
// when kloset reads it.
func hostFileRecord(pathname, hostpath string, st os.FileInfo) *connectors.Record {
	finfo := objects.FileInfo{
		Lname:    path.Base(pathname),
		Lsize:    st.Size(),
		Lmode:    0644,
		LmodTime: st.ModTime(),
	}
	return connectors.NewRecord(pathname, "", finfo, nil, func() (io.ReadCloser, error) {
		return os.Open(hostpath)
	})
}
