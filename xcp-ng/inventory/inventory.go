package inventory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-xcp-ng/xcp-ng/xoa"
)

type xcpng struct {
	xo *xoa.Client
}

func NewInventory(ctx context.Context, params map[string]string) (integration.Inventory, error) {
	if params["xo_url"] == "" {
		return nil, errors.New("xo_url is required")
	}
	if params["xo_token"] == "" {
		return nil, errors.New("xo_token is required")
	}
	skipVerify := false
	if v := params["xo_tls_skip_verify"]; v != "" {
		var err error
		if skipVerify, err = strconv.ParseBool(v); err != nil {
			return nil, fmt.Errorf("invalid xo_tls_skip_verify %q: %w", v, err)
		}
	}

	xo, err := xoa.New(xoa.Config{
		URL:                params["xo_url"],
		Token:              params["xo_token"],
		InsecureSkipVerify: skipVerify,
	})
	if err != nil {
		return nil, fmt.Errorf("invalid xo_url: %w", err)
	}

	// The SDK has no Ping: fail here on a bad URL or token rather than on
	// the first List.
	if _, err := xo.Pools(ctx); err != nil {
		return nil, fmt.Errorf("cannot reach xen orchestra: %w", err)
	}

	return &xcpng{xo: xo}, nil
}

func (x *xcpng) List(ctx context.Context, resources chan<- *integration.InventoryEntry) error {
	defer close(resources)

	pools, err := x.xo.Pools(ctx)
	if err != nil {
		return err
	}
	poolNames := make(map[string]string, len(pools))
	for _, p := range pools {
		poolNames[p.UUID] = p.NameLabel
	}

	vms, err := x.xo.VMs(ctx)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		select {
		case resources <- vmEntry(vm, poolNames[vm.PoolUUID]):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func vmEntry(vm xoa.VM, poolName string) *integration.InventoryEntry {
	endpoints := []integration.HostEndpoint{{Type: integration.EndpointUUID, Endpoint: vm.UUID}}
	for _, ip := range vm.IPs() {
		// Only unicast addresses can be backup targets. Link-local and
		// loopback ones are not reachable from Plakar either.
		if !ip.IsGlobalUnicast() {
			continue
		}
		typ := integration.EndpointInet6
		if ip.Is4() {
			typ = integration.EndpointInet4
		}
		endpoints = append(endpoints, integration.HostEndpoint{Type: typ, Endpoint: ip.String()})
	}

	return &integration.InventoryEntry{
		Class:    integration.ResourceClassCompute,
		SubClass: integration.ResourceSubClassUndefined,
		// XO returns an empty tag on some VMs.
		Tags: slices.DeleteFunc(slices.Clone(vm.Tags), func(t string) bool { return t == "" }),
		// UUIDs are unique across XAPI, so XO itself is not part of the URN.
		URN:       fmt.Sprintf("urn:xcp-ng:pool:%s:vm:%s", vm.PoolUUID, vm.UUID),
		Name:      vm.NameLabel,
		Region:    poolName,
		Service:   "instance",
		Resource:  "compute:instance",
		Endpoints: endpoints,
	}
}

func (x *xcpng) Close(ctx context.Context) error { return nil }
