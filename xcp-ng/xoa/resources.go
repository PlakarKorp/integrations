package xoa

import (
	"context"
	"net/url"
)

type Pool struct {
	UUID      string `json:"uuid"`
	NameLabel string `json:"name_label"`
}

func (c *Client) Pools(ctx context.Context) ([]Pool, error) {
	return c.get[[]Pool](ctx, "pools", url.Values{"fields": {"uuid,name_label"}})
}

type VM struct {
	UUID       string   `json:"uuid"`
	NameLabel  string   `json:"name_label"`
	PowerState string   `json:"power_state"`
	Tags       []string `json:"tags"`
	// Addresses is filled by the guest tools, keyed like "0/ipv4/0".
	Addresses map[string]string `json:"addresses"`
	PoolUUID  string            `json:"$pool"`
}

func (c *Client) VMs(ctx context.Context) ([]VM, error) {
	return c.get[[]VM](ctx, "vms", url.Values{"fields": {"uuid,name_label,power_state,tags,addresses,$pool"}})
}
