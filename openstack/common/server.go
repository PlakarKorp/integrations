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
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
)

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
