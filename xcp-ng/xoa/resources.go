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
