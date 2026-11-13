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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/moby/moby/client"
)

// relay owns the channels kloset gave us.  Records go out without
// waiting: on an incremental backup kloset holds records in a prefetch
// window and only releases them when the window fills or the stream
// closes, so an importer that waits for an acknowledgment mid-stream
// never gets one.  Results are counted as they come back, and handed to
// the helper importer feeding volume data when one is active, since that
// importer keeps the content readers open until it sees them.
type relay struct {
	records chan<- *connectors.Record
	sent    atomic.Uint64
	recv    atomic.Uint64
	drained chan struct{}

	mu    sync.Mutex
	inner chan<- *connectors.Result
}

func newRelay(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) *relay {
	r := &relay{records: records, drained: make(chan struct{})}
	go func() {
		defer close(r.drained)
		for {
			select {
			case <-ctx.Done():
				return
			case res, ok := <-results:
				if !ok {
					return
				}
				r.recv.Add(1)
				r.mu.Lock()
				inner := r.inner
				r.mu.Unlock()
				if inner != nil {
					select {
					case inner <- res:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return r
}

func (r *relay) send(ctx context.Context, rec *connectors.Record) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r.records <- rec:
		r.sent.Add(1)
		return nil
	}
}

func (r *relay) setInner(ch chan<- *connectors.Result) {
	r.mu.Lock()
	r.inner = ch
	r.mu.Unlock()
}

// wait blocks until kloset acknowledged every record, or closed its side.
// Only valid once records has been closed.
func (r *relay) wait(ctx context.Context) error {
	for {
		if r.recv.Load() >= r.sent.Load() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.drained:
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (d *Docker) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	r := newRelay(ctx, records, results)

	// the helpers must outlive the walk: kloset reads the volume data
	// through them after the records went by.
	defer d.stopHelpers(ctx)

	err := d.walk(ctx, r)
	close(records)
	if err != nil {
		return err
	}
	return r.wait(ctx)
}

func (d *Docker) walk(ctx context.Context, r *relay) error {
	if d.project != "" {
		if err := d.loadProject(ctx); err != nil {
			return err
		}
	}
	if d.kind == "" {
		rec, err := d.manifest(ctx)
		if err != nil {
			return err
		}
		if err := r.send(ctx, rec); err != nil {
			return err
		}
	}

	if d.wants("images", "") {
		if err := d.walkImages(ctx, r); err != nil {
			return err
		}
	}
	if d.wants("networks", "") {
		if err := d.walkNetworks(ctx, r); err != nil {
			return err
		}
	}
	if d.wants("volumes", "") {
		if err := d.walkVolumes(ctx, r); err != nil {
			return err
		}
	}
	if d.wants("containers", "") {
		if err := d.walkContainers(ctx, r); err != nil {
			return err
		}
	}
	if d.project != "" {
		return d.walkCompose(ctx, r)
	}
	return nil
}

// filter narrows a list to the compose project when walking one.
func (d *Docker) filter() client.Filters {
	if d.project == "" {
		return nil
	}
	return d.projectFilter()
}

func (d *Docker) manifest(ctx context.Context) (*connectors.Record, error) {
	ver, err := d.cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return nil, fmt.Errorf("server version: %w", err)
	}
	m := Manifest{
		Version:       manifestVersion,
		CreatedAt:     time.Now().UTC(),
		Hostname:      d.host,
		ServerVersion: ver.Version,
		APIVersion:    ver.APIVersion,
		Platform:      ver.Platform.Name,
		OS:            ver.Os,
		Architecture:  ver.Arch,

		ComposeVersion: d.proj.Version,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return fileRecord("/manifest.json", data, m.CreatedAt), nil
}

func (d *Docker) walkImages(ctx context.Context, r *relay) error {
	if d.project != "" {
		return d.walkProjectImages(ctx, r)
	}
	list, err := d.cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return fmt.Errorf("image list: %w", err)
	}
	for _, img := range list.Items {
		for _, ref := range img.RepoTags {
			if ref == "<none>:<none>" {
				continue
			}
			repo, tag := splitRef(ref)
			if !d.wants("images", repo+"/"+tag) {
				continue
			}
			p := path.Join("/images", repo, tag+".tar")
			ref := ref
			rec := streamRecord(p, time.Unix(img.Created, 0), func() (io.ReadCloser, error) {
				return d.cli.ImageSave(ctx, []string{ref})
			})
			if err := r.send(ctx, rec); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Docker) walkNetworks(ctx context.Context, r *relay) error {
	list, err := d.cli.NetworkList(ctx, client.NetworkListOptions{Filters: d.filter()})
	if err != nil {
		return fmt.Errorf("network list: %w", err)
	}
	for _, n := range list.Items {
		if !d.wants("networks", n.Name) {
			continue
		}
		p := path.Join("/networks", n.Name+".json")
		ins, err := d.cli.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if err != nil {
			if err := r.send(ctx, connectors.NewError(p, err)); err != nil {
				return err
			}
			continue
		}
		if err := r.send(ctx, fileRecord(p, ins.Raw, n.Created)); err != nil {
			return err
		}
	}
	return nil
}

func (d *Docker) walkVolumes(ctx context.Context, r *relay) error {
	list, err := d.cli.VolumeList(ctx, client.VolumeListOptions{Filters: d.filter()})
	if err != nil {
		return fmt.Errorf("volume list: %w", err)
	}
	for _, v := range list.Items {
		if !d.wants("volumes", v.Name) {
			continue
		}
		base := path.Join("/volumes", v.Name)
		ins, err := d.cli.VolumeInspect(ctx, v.Name, client.VolumeInspectOptions{})
		if err != nil {
			if err := r.send(ctx, connectors.NewError(base, err)); err != nil {
				return err
			}
			continue
		}
		created, _ := time.Parse(time.RFC3339, v.CreatedAt)
		if err := r.send(ctx, fileRecord(path.Join(base, "config.json"), ins.Raw, created)); err != nil {
			return err
		}

		if err := d.volumeData(ctx, r, v.Name, path.Join(base, "data")); err != nil {
			if ctx.Err() != nil {
				return err
			}
			if err := r.send(ctx, connectors.NewError(path.Join(base, "data"), err)); err != nil {
				return err
			}
		}
	}
	return nil
}

// volumeData streams the content of volume vol under prefix, pausing the
// running containers that use it when asked to.  The helper stays up
// until the end of the backup, since kloset reads the content later.
func (d *Docker) volumeData(ctx context.Context, r *relay, vol, prefix string) error {
	if d.pause {
		unpause, err := d.pauseUsers(ctx, vol)
		if err != nil {
			return err
		}
		// TODO: the containers resume once the records went by, not
		// once kloset read the data; for a consistent copy the pause
		// has to last until the helper is stopped.
		defer unpause()
	}

	h, err := d.startHelper(ctx, vol, true)
	if err != nil {
		return err
	}
	d.helpers = append(d.helpers, h)

	return d.consume(ctx, r, h, prefix)
}

func (d *Docker) stopHelpers(ctx context.Context) {
	for _, h := range d.helpers {
		h.stop(ctx)
	}
	d.helpers = nil
}

// pauseUsers pauses the running containers that mount vol and returns the
// function that resumes them.
func (d *Docker) pauseUsers(ctx context.Context, vol string) (func(), error) {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		Filters: client.Filters{}.Add("volume", vol).Add("status", "running"),
	})
	if err != nil {
		return nil, fmt.Errorf("container list: %w", err)
	}

	var paused []string
	unpause := func() {
		for _, id := range paused {
			_, _ = d.cli.ContainerUnpause(context.WithoutCancel(ctx), id, client.ContainerUnpauseOptions{})
		}
	}
	for _, c := range list.Items {
		if _, err := d.cli.ContainerPause(ctx, c.ID, client.ContainerPauseOptions{}); err != nil {
			unpause()
			return nil, fmt.Errorf("pause %s: %w", strings.Join(c.Names, ","), err)
		}
		paused = append(paused, c.ID)
	}
	return unpause, nil
}

func (d *Docker) walkContainers(ctx context.Context, r *relay) error {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: d.filter()})
	if err != nil {
		return fmt.Errorf("container list: %w", err)
	}
	for _, c := range list.Items {
		if _, ok := c.Labels[helperLabel]; ok {
			continue
		}
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		if !d.wants("containers", name) {
			continue
		}
		p := path.Join("/containers", name, "config.json")
		ins, err := d.cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			if err := r.send(ctx, connectors.NewError(p, err)); err != nil {
				return err
			}
			continue
		}
		if err := r.send(ctx, fileRecord(p, ins.Raw, time.Unix(c.Created, 0))); err != nil {
			return err
		}
	}
	return nil
}
