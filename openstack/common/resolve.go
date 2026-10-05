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

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
)

// ResolveFlavor finds flavorID on the restore target, falling back to
// flavorName when the ID no longer exists there (e.g. a different project
// or region than the one backed up).
func (c *Client) ResolveFlavor(ctx context.Context, flavorID, flavorName string) (*flavors.Flavor, error) {
	nova, err := c.nova()
	if err != nil {
		return nil, err
	}
	if flavorID != "" {
		if flavor, err := flavors.Get(ctx, nova, flavorID).Extract(); err == nil {
			return flavor, nil
		}
	}

	pages, err := flavors.ListDetail(nova, nil).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list flavors: %w", err)
	}
	all, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return nil, fmt.Errorf("list flavors: %w", err)
	}
	var found *flavors.Flavor
	for i, flavor := range all {
		if flavor.Name == flavorName {
			if found != nil {
				return nil, fmt.Errorf("flavor %q: multiple matches", flavorName)
			}
			found = &all[i]
		}
	}
	if found == nil {
		return nil, fmt.Errorf("flavor %q: not found", flavorName)
	}
	return found, nil
}

// ResolveNetwork finds networkID on the restore target, falling back to
// networkName when the ID no longer exists there.
func (c *Client) ResolveNetwork(ctx context.Context, networkID, networkName string) (*networks.Network, error) {
	neutron, err := c.neutron()
	if err != nil {
		return nil, err
	}
	if networkID != "" {
		if network, err := networks.Get(ctx, neutron, networkID).Extract(); err == nil {
			return network, nil
		}
	}

	pages, err := networks.List(neutron, networks.ListOpts{Name: networkName}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list networks named %q: %w", networkName, err)
	}
	matches, err := networks.ExtractNetworks(pages)
	if err != nil {
		return nil, fmt.Errorf("list networks named %q: %w", networkName, err)
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("network %q: not found", networkName)
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("network %q: multiple matches", networkName)
	}
}
