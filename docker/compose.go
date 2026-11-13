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

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/moby/moby/client"
)

// labels compose sets on every object of a project.
const (
	composeProjectLabel = "com.docker.compose.project"
	composeFilesLabel   = "com.docker.compose.project.config_files"
	composeDirLabel     = "com.docker.compose.project.working_dir"
	composeVersionLabel = "com.docker.compose.version"
)

// Project records where a compose project's files were found on the host
// at backup time; it is written as /compose/<project>/project.json.
type Project struct {
	Name        string   `json:"name"`
	WorkingDir  string   `json:"working_dir"`
	ConfigFiles []string `json:"config_files"`
	Version     string   `json:"compose_version,omitempty"`
}

// projectFilter selects the daemon objects that belong to the project.
func (d *Docker) projectFilter() client.Filters {
	return client.Filters{}.Add("label", composeProjectLabel+"="+d.project)
}

// loadProject locates the project's compose files from the labels of its
// containers, renders the configuration with the compose plugin and
// collects the images it names.
func (d *Docker) loadProject(ctx context.Context) error {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: d.projectFilter()})
	if err != nil {
		return fmt.Errorf("container list: %w", err)
	}
	if len(list.Items) == 0 {
		return fmt.Errorf("compose project %s has no container on this daemon, cannot locate its files", d.project)
	}
	// containers left over from an earlier up may name other files; the
	// newest one says what the project was last started from.
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Created > list.Items[j].Created })
	labels := list.Items[0].Labels
	d.proj = Project{
		Name:       d.project,
		WorkingDir: labels[composeDirLabel],
		Version:    labels[composeVersionLabel],
	}
	if files := labels[composeFilesLabel]; files != "" {
		d.proj.ConfigFiles = strings.Split(files, ",")
	}
	if d.proj.WorkingDir == "" || len(d.proj.ConfigFiles) == 0 {
		return fmt.Errorf("compose project %s: its containers carry no config_files label", d.project)
	}

	d.rendered, err = d.compose(ctx, "config")
	if err != nil {
		return err
	}
	images, err := d.compose(ctx, "config", "--images")
	if err != nil {
		return err
	}
	d.images = map[string]bool{}
	for _, ref := range strings.Fields(string(images)) {
		repo, tag := splitRef(ref)
		d.images[repo+":"+tag] = true
	}
	return nil
}

// compose runs the compose plugin on the project's own files, from its
// working directory, so that .env and relative paths resolve as they did
// when the project was started.
func (d *Docker) compose(ctx context.Context, args ...string) ([]byte, error) {
	argv := []string{"compose", "--project-name", d.proj.Name, "--project-directory", d.proj.WorkingDir}
	for _, f := range d.proj.ConfigFiles {
		argv = append(argv, "--file", f)
	}
	argv = append(argv, args...)
	return runDocker(ctx, argv...)
}

func runDocker(ctx context.Context, argv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// walkProjectImages sends the images the rendered configuration names.
func (d *Docker) walkProjectImages(ctx context.Context, r *relay) error {
	refs := make([]string, 0, len(d.images))
	for ref := range d.images {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		repo, tag := splitRef(ref)
		p := path.Join("/images", repo, tag+".tar")
		ins, err := d.cli.ImageInspect(ctx, ref)
		if err != nil {
			if err := r.send(ctx, connectors.NewError(p, err)); err != nil {
				return err
			}
			continue
		}
		created, _ := time.Parse(time.RFC3339Nano, ins.Created)
		rec := streamRecord(p, created, func() (io.ReadCloser, error) {
			return d.cli.ImageSave(ctx, []string{ref})
		})
		if err := r.send(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

// walkCompose sends the rendered configuration, the project description
// and the files the project was started from, under /compose/<project>/.
func (d *Docker) walkCompose(ctx context.Context, r *relay) error {
	base := path.Join("/compose", d.project)
	now := time.Now().UTC()

	meta, err := json.MarshalIndent(d.proj, "", "  ")
	if err != nil {
		return err
	}
	if err := r.send(ctx, fileRecord(path.Join(base, "project.json"), meta, now)); err != nil {
		return err
	}
	if err := r.send(ctx, fileRecord(path.Join(base, "compose.yaml"), d.rendered, now)); err != nil {
		return err
	}

	// the original files, for the record; .env is read by compose when
	// present and is not named in the labels.
	files := append([]string(nil), d.proj.ConfigFiles...)
	files = append(files, filepath.Join(d.proj.WorkingDir, ".env"))
	for _, f := range files {
		p := path.Join(base, "files", filepath.ToSlash(f))
		st, err := os.Stat(f)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && filepath.Base(f) == ".env" {
				continue
			}
			if err := r.send(ctx, connectors.NewError(p, err)); err != nil {
				return err
			}
			continue
		}
		if err := r.send(ctx, hostFileRecord(p, f, st)); err != nil {
			return err
		}
	}
	return nil
}

// restoreComposeFile writes one entry of /compose/<project>/ under the
// restore directory.
func (d *Docker) restoreComposeFile(rest string, rec *connectors.Record) error {
	if d.dir == "" {
		return errors.New("compose files restore with -to docker+compose:/<dir>")
	}
	target := filepath.Join(d.dir, filepath.FromSlash(rest))
	if rec.FileInfo.Lmode.IsDir() {
		return os.MkdirAll(target, 0755)
	}
	if rec.Reader == nil {
		return errors.New("record has no content")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rec.Reader); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if rest == "compose.yaml" {
		d.composeFile = target
	}
	return nil
}

// composeUp brings the project up from the restored configuration.  The
// images came from the snapshot, so nothing is built.
func (d *Docker) composeUp(ctx context.Context) error {
	if d.composeFile == "" {
		return errors.New("no compose.yaml in the restored snapshot")
	}
	out, err := runDocker(ctx, "compose", "--file", d.composeFile, "up", "--detach", "--no-build")
	if err != nil {
		return err
	}
	if d.opts != nil && d.opts.Stderr != nil && len(out) > 0 {
		fmt.Fprintf(d.opts.Stderr, "%s", out)
	}
	return nil
}
