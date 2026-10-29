/*
 * Copyright (c) 2026 Peeyush Aggarwal <peeyushaggarwal94@gmail.com>
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

// Package attach is the host's side of reading a Cinder volume from a local
// block device: finding out whether the host plakar runs on is a Nova server
// a volume can be attached to, and finding the device an attached volume
// turns up as. The OpenStack calls are the common package's.
package attach

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
)

// ErrNotAttachable is Preflight's error when the host or the volume rules
// attaching out. Any other error means Preflight could not tell.
var ErrNotAttachable = errors.New("cannot attach to this host")

// How the host learns which Nova server it is.
var (
	// MetadataURL is the metadata service Nova serves every server at a
	// link-local address. A variable so tests can point it at a fake.
	// TODO: Should this be a configurable option?
	MetadataURL = "http://169.254.169.254/openstack/latest/meta_data.json"
	// metadataTimeout bounds the request: off-cloud, the address usually
	// drops packets rather than refusing the connection.
	metadataTimeout = 3 * time.Second
	// dmiDir holds the SMBIOS fields the hypervisor stamps on the server,
	// the fallback when the metadata service is unreachable. Linux only.
	dmiDir = "/sys/class/dmi/id"
)

// metadataClient bypasses any proxy: the metadata service is link-local.
var metadataClient = &http.Client{Transport: &http.Transport{Proxy: nil}}

// statusActive is a running Nova server's status.
const statusActive = "ACTIVE"

// Nova is what Preflight asks of the cloud: the server the host claims to
// be. A 404 must be a gophercloud error, as common.Client's is.
type Nova interface {
	GetServer(ctx context.Context, serverID string) (*servers.Server, error)
}

// Preflight identifies the executing host through the metadata service or DMI
// and verifies that it is an active Nova server in the credential's project.
func Preflight(ctx context.Context, nova Nova, projectID string) (*servers.Server, error) {
	host, err := identify(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: not a Nova server: %w", ErrNotAttachable, err)
	}
	if host.Project != "" && host.Project != projectID {
		return nil, fmt.Errorf("%w: server %q is in project %q, the credential in %q", ErrNotAttachable, host.ID, host.Project, projectID)
	}

	server, err := nova.GetServer(ctx, host.ID)
	switch {
	case gophercloud.ResponseCodeIs(err, http.StatusNotFound):
		return nil, fmt.Errorf("%w: server %q (from %s) is not in this cloud, project or region", ErrNotAttachable, host.ID, host.Source)
	case err != nil:
		return nil, err
	case server.Status != statusActive:
		return nil, fmt.Errorf("%w: server %q is %s", ErrNotAttachable, server.ID, server.Status)
	}
	return server, nil
}

// identity is what the host knows about itself as a Nova server.
type identity struct {
	ID      string
	Project string // empty when the source does not tell
	Source  string // where it came from, for messages
}

// errUnreachable marks a metadata service that did not answer at all, as
// opposed to one that answered 404: another cloud's does.
var errUnreachable = errors.New("unreachable")

// identify asks the metadata service, and when that is unreachable (some
// clouds disable it in favour of a config drive), reads the SMBIOS fields. An
// answer from the metadata service is final either way.
func identify(ctx context.Context) (*identity, error) {
	host, err := fromMetadata(ctx)
	if err == nil || !errors.Is(err, errUnreachable) {
		return nil, err
	}

	host, dmiErr := fromDMI()
	if dmiErr != nil {
		return nil, fmt.Errorf("%w; DMI: %w", err, dmiErr)
	}
	return host, nil
}

func fromMetadata(ctx context.Context) (*identity, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, MetadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("metadata service: %w", err)
	}
	resp, err := metadataClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata service: %w: %w", errUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata service: %s", resp.Status)
	}

	var meta struct {
		UUID      string `json:"uuid"`
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return nil, fmt.Errorf("metadata service: decode: %w", err)
	}
	if meta.UUID == "" {
		return nil, errors.New("metadata service: no uuid")
	}
	return &identity{ID: meta.UUID, Project: meta.ProjectID, Source: "the metadata service"}, nil
}

// fromDMI reads the SMBIOS system UUID, which Nova's libvirt driver sets to
// the server's ID, once the product name says Nova. Linux only, and the UUID
// is readable by root only.
func fromDMI() (*identity, error) {
	product, err := os.ReadFile(filepath.Join(dmiDir, "product_name"))
	if err != nil {
		return nil, err
	}
	if name := strings.TrimSpace(string(product)); !strings.Contains(name, "OpenStack") {
		return nil, fmt.Errorf("product %q is not OpenStack", name)
	}
	uuid, err := os.ReadFile(filepath.Join(dmiDir, "product_uuid"))
	if err != nil {
		return nil, err
	}
	id := strings.ToLower(strings.TrimSpace(string(uuid)))
	if id == "" {
		return nil, errors.New("product_uuid is empty")
	}
	return &identity{ID: id, Source: "DMI"}, nil
}
