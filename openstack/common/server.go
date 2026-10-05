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

	"github.com/gophercloud/gophercloud/v2"
	gcopenstack "github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/attachinterfaces"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
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
)

func (c *Client) nova() (*gophercloud.ServiceClient, error) {
	return c.service(serviceServers, gcopenstack.NewComputeV2)
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
