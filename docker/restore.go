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
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// networks the daemon owns; they exist on every host and cannot be created.
var builtinNetworks = map[string]bool{
	"bridge": true, "host": true, "none": true,
	"ingress": true, "docker_gwbridge": true,
}

// Export recreates the snapshot's objects on the daemon.  Records arrive in
// walk order, so a volume's config.json comes before its data and the data
// of one volume is contiguous, which is what the single restore helper
// relies on.
func (d *Docker) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for {
		var rec *connectors.Record
		var ok bool
		select {
		case <-ctx.Done():
			return ctx.Err()
		case rec, ok = <-records:
			if !ok {
				return nil
			}
		}

		if rec.Err != nil {
			results <- rec.Ok()
			continue
		}

		closed, err := d.restoreRecord(ctx, rec)
		switch {
		case closed:
			// the helper exporter closed the reader; the SDK's reader
			// does not survive a second close.
			results <- &connectors.Result{Record: *rec, Err: err}
		case err != nil:
			results <- rec.Error(err)
		default:
			results <- rec.Ok()
		}
	}
}

// readAll reads a record's content.  The reader is closed by the result
// sent back to kloset, and the SDK's reader does not survive a second
// close.
func readAll(rec *connectors.Record) ([]byte, error) {
	if rec.Reader == nil {
		return nil, errors.New("record has no content")
	}
	return io.ReadAll(rec.Reader)
}

// restoreRecord applies one record.  closed reports that the record's
// reader was already closed, by the helper exporter it was handed to.
func (d *Docker) restoreRecord(ctx context.Context, rec *connectors.Record) (closed bool, err error) {
	p := path.Join(d.base, path.Clean(rec.Pathname))
	segs := strings.SplitN(strings.Trim(p, "/"), "/", 3)
	kind := segs[0]

	// anything that is not volume data ends the volume being restored.
	if kind != "volumes" && d.cur != nil {
		if err := d.cur.finish(ctx); err != nil {
			d.cur = nil
			return false, err
		}
		d.cur = nil
	}

	if rec.IsXattr {
		if kind == "volumes" && len(segs) == 3 && strings.HasPrefix(segs[2], "data") {
			return true, d.restoreVolumeData(ctx, segs[1], segs[2], rec)
		}
		return false, nil
	}

	switch {
	case p == "/":
		return false, nil

	case p == "/manifest.json":
		data, err := readAll(rec)
		if err != nil {
			return false, err
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return false, fmt.Errorf("manifest: %w", err)
		}
		if m.Version != manifestVersion {
			return false, fmt.Errorf("manifest version %d, want %d", m.Version, manifestVersion)
		}
		return false, nil

	case kind == "images":
		if rec.FileInfo.Lmode.IsDir() {
			return false, nil
		}
		return false, d.restoreImage(ctx, rec)

	case kind == "networks":
		if rec.FileInfo.Lmode.IsDir() {
			return false, nil
		}
		return false, d.restoreNetwork(ctx, rec)

	case kind == "volumes":
		if len(segs) < 3 {
			return false, nil
		}
		name, rest := segs[1], segs[2]
		switch {
		case rest == "config.json":
			return false, d.restoreVolume(ctx, name, rec)
		case rest == "data" || strings.HasPrefix(rest, "data/"):
			return true, d.restoreVolumeData(ctx, name, rest, rec)
		default:
			return false, fmt.Errorf("unexpected %s under volume %s", rest, name)
		}

	case kind == "containers":
		if len(segs) < 3 {
			return false, nil
		}
		if segs[2] != "config.json" {
			return false, fmt.Errorf("unexpected %s under container %s", segs[2], segs[1])
		}
		return false, d.restoreContainer(ctx, rec)

	default:
		return false, fmt.Errorf("unexpected path %s", p)
	}
}

func (d *Docker) restoreImage(ctx context.Context, rec *connectors.Record) error {
	if rec.Reader == nil {
		return errors.New("record has no content")
	}
	// the request body gets closed by the HTTP client; the result sent
	// back to kloset closes the reader, and only once.
	res, err := d.cli.ImageLoad(ctx, io.NopCloser(rec.Reader))
	if err != nil {
		return fmt.Errorf("image load: %w", err)
	}
	defer res.Close()
	// the daemon reports progress as a JSON stream; it has to be read to
	// the end for the load to complete.
	if _, err := io.Copy(io.Discard, res); err != nil {
		return fmt.Errorf("image load: %w", err)
	}
	return nil
}

func (d *Docker) restoreNetwork(ctx context.Context, rec *connectors.Record) error {
	data, err := readAll(rec)
	if err != nil {
		return err
	}
	var n network.Inspect
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	if builtinNetworks[n.Name] {
		return nil
	}
	if _, err := d.cli.NetworkInspect(ctx, n.Name, client.NetworkInspectOptions{}); err == nil {
		// already there, leave it alone
		return nil
	}
	ipam := n.IPAM
	_, err = d.cli.NetworkCreate(ctx, n.Name, client.NetworkCreateOptions{
		Driver:     n.Driver,
		Scope:      n.Scope,
		EnableIPv4: &n.EnableIPv4,
		EnableIPv6: &n.EnableIPv6,
		IPAM:       &ipam,
		Internal:   n.Internal,
		Attachable: n.Attachable,
		Ingress:    n.Ingress,
		ConfigOnly: n.ConfigOnly,
		ConfigFrom: n.ConfigFrom.Network,
		Options:    n.Options,
		Labels:     n.Labels,
	})
	if err != nil {
		return fmt.Errorf("network create %s: %w", n.Name, err)
	}
	return nil
}

func (d *Docker) restoreVolume(ctx context.Context, name string, rec *connectors.Record) error {
	data, err := readAll(rec)
	if err != nil {
		return err
	}
	var v volume.Volume
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("volume: %w", err)
	}
	if v.Name == "" {
		v.Name = name
	}
	// creating a volume that exists with the same name is a no-op for
	// the daemon, which returns the existing one.
	_, err = d.cli.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name:       v.Name,
		Driver:     v.Driver,
		DriverOpts: v.Options,
		Labels:     v.Labels,
	})
	if err != nil {
		return fmt.Errorf("volume create %s: %w", v.Name, err)
	}
	return nil
}

func (d *Docker) restoreVolumeData(ctx context.Context, vol, rest string, rec *connectors.Record) error {
	rh, err := d.restoreInto(ctx, vol)
	if err != nil {
		return err
	}
	out := *rec
	out.Pathname = path.Join("/", strings.TrimPrefix(rest, "data"))
	if out.Pathname == "/" {
		out.FileInfo.Lname = "/"
	}
	return rh.put(ctx, &out)
}

func (d *Docker) restoreContainer(ctx context.Context, rec *connectors.Record) error {
	data, err := readAll(rec)
	if err != nil {
		return err
	}
	var c container.InspectResponse
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("container: %w", err)
	}
	name := strings.TrimPrefix(c.Name, "/")
	if name == "" {
		return errors.New("container has no name")
	}
	if _, err := d.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); err == nil {
		return fmt.Errorf("container %s already exists", name)
	}

	// endpoints carry the IDs and addresses the old daemon handed out;
	// keep only what the user asked for.
	netcfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	if c.NetworkSettings != nil {
		for n, ep := range c.NetworkSettings.Networks {
			if ep == nil {
				continue
			}
			netcfg.EndpointsConfig[n] = &network.EndpointSettings{
				IPAMConfig: ep.IPAMConfig,
				Links:      ep.Links,
				Aliases:    ep.Aliases,
				DriverOpts: ep.DriverOpts,
				GwPriority: ep.GwPriority,
			}
		}
	}

	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:             name,
		Config:           c.Config,
		HostConfig:       c.HostConfig,
		NetworkingConfig: netcfg,
	})
	if err != nil {
		return fmt.Errorf("container create %s: %w", name, err)
	}
	if d.start {
		if _, err := d.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("container start %s: %w", name, err)
		}
	}
	return nil
}
