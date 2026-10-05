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

package common

import (
	"context"
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	gcopenstack "github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/attachinterfaces"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/volumeattach"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
)

// ServerMetadata is the /.METADATA.json of an instance snapshot's server
// entry. Its JSON is a persisted format: do not rename or drop fields.
type ServerMetadata struct {
	Server   *servers.Server     `json:"server"`
	Flavor   *flavors.Flavor     `json:"flavor"`
	Networks []*networks.Network `json:"networks"`
}

const (
	serviceServers  = "servers"
	serviceNetworks = "networks"

	kindServer = "server"

	statusServerActive = "ACTIVE"
	statusServerError  = "ERROR"
)

// novaMicroversion is the minimum that accepts networks: "none" on server
// create (restore, with no saved network to attach).
const novaMicroversion = "2.37"

func (c *Client) nova() (*gophercloud.ServiceClient, error) {
	nova, err := c.service(serviceServers, gcopenstack.NewComputeV2)
	if err != nil {
		return nil, err
	}
	nova.Microversion = novaMicroversion
	return nova, nil
}

func (c *Client) neutron() (*gophercloud.ServiceClient, error) {
	return c.service(serviceNetworks, gcopenstack.NewNetworkV2)
}

func (c *Client) GetServer(ctx context.Context, serverID string) (*servers.Server, error) {
	nova, err := c.nova()
	if err != nil {
		return nil, err
	}
	server, err := servers.Get(ctx, nova, serverID).Extract()
	if err != nil {
		return nil, fmt.Errorf("get server %q: %w", serverID, err)
	}
	return server, nil
}

// ServerMetadata reads what a restore needs to recreate the server: its
// flavor and the networks of its interfaces, in interface order.
func (c *Client) ServerMetadata(ctx context.Context, server *servers.Server) (*ServerMetadata, error) {
	nova, err := c.nova()
	if err != nil {
		return nil, err
	}
	flavorID, _ := server.Flavor["id"].(string)
	if flavorID == "" {
		return nil, fmt.Errorf("server %q: flavor id missing", server.ID)
	}
	flavor, err := flavors.Get(ctx, nova, flavorID).Extract()
	if err != nil {
		return nil, fmt.Errorf("get flavor %q: %w", flavorID, err)
	}

	page, err := attachinterfaces.List(nova, server.ID).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list interfaces of server %q: %w", server.ID, err)
	}
	ifaces, err := attachinterfaces.ExtractInterfaces(page)
	if err != nil {
		return nil, fmt.Errorf("list interfaces of server %q: %w", server.ID, err)
	}

	neutron, err := c.neutron()
	if err != nil {
		return nil, err
	}
	nets := make([]*networks.Network, 0, len(ifaces))
	for _, iface := range ifaces {
		net, err := networks.Get(ctx, neutron, iface.NetID).Extract()
		if err != nil {
			return nil, fmt.Errorf("get network %q: %w", iface.NetID, err)
		}
		nets = append(nets, net)
	}

	return &ServerMetadata{Server: server, Flavor: flavor, Networks: nets}, nil
}

// CreateServerImage snapshots the server's root disk into a Glance image.
// Refuses a boot-from-volume server (no root disk to snapshot): its
// server.Image has no "id", and as a second guard, the image Nova still
// creates for one carries a "block_device_mapping" property. The image goes
// into cleanup as soon as it exists, so a failure leaves nothing behind once
// cleanup runs.
func (c *Client) CreateServerImage(ctx context.Context, server *servers.Server, cleanup *Cleanup) (*images.Image, error) {
	if id, _ := server.Image["id"].(string); id == "" {
		return nil, fmt.Errorf("server %q: boot-from-volume servers are not supported", server.ID)
	}
	nova, err := c.nova()
	if err != nil {
		return nil, err
	}
	image, err := c.glance()
	if err != nil {
		return nil, err
	}
	cleanup.image = image

	imageID, err := servers.CreateImage(ctx, nova, server.ID, servers.CreateImageOpts{
		Name: fmt.Sprintf("%s%s-%d", backupPrefix, server.ID, time.Now().UTC().UnixNano()),
	}).ExtractImageID()
	if err != nil {
		return nil, fmt.Errorf("snapshot server %q: %w", server.ID, err)
	}
	cleanup.imageID = imageID

	img, err := waitStatus(ctx, kindImage, imageID, getImage(image, imageID), statusActive, statusKilled, statusError)
	if err != nil {
		return nil, err
	}
	if _, ok := img.Properties["block_device_mapping"]; ok {
		return nil, fmt.Errorf("server %q: boot-from-volume servers are not supported", server.ID)
	}
	return img, nil
}

func getServer(nova *gophercloud.ServiceClient, id string) getter[*servers.Server] {
	return func(ctx context.Context) (*servers.Server, string, error) {
		s, err := servers.Get(ctx, nova, id).Extract()
		if err != nil {
			return nil, "", err
		}
		return s, s.Status, nil
	}
}

// CreateServer spawns a server and waits for it to become active.
func (c *Client) CreateServer(ctx context.Context, opts servers.CreateOpts) (*servers.Server, error) {
	nova, err := c.nova()
	if err != nil {
		return nil, err
	}
	// networks: "none" (set by the caller for a restore with no saved
	// network) needs this microversion; nothing else here does.
	nova.Microversion = novaMicroversion
	created, err := servers.Create(ctx, nova, opts, nil).Extract()
	if err != nil {
		return nil, fmt.Errorf("create server %q: %w", opts.Name, err)
	}
	server, err := waitStatus(ctx, kindServer, created.ID, getServer(nova, created.ID), statusServerActive, statusServerError)
	if err != nil {
		return nil, err
	}
	return server, nil
}

// AttachVolume attaches volumeID to serverID. device is the original
// attachment's device path (e.g. /dev/vdb), or "" to let Nova pick one.
func (c *Client) AttachVolume(ctx context.Context, serverID, volumeID, device string) error {
	nova, err := c.nova()
	if err != nil {
		return err
	}
	_, err = volumeattach.Create(ctx, nova, serverID, volumeattach.CreateOpts{
		VolumeID: volumeID,
		Device:   device,
	}).Extract()
	if err != nil {
		return fmt.Errorf("attach volume %q to server %q: %w", volumeID, serverID, err)
	}
	return nil
}
