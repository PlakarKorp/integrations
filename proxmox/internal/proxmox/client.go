/*
 * Copyright (c) 2026 Gilles DUBOIS
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

package proxmox

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Client struct {
	cfg    *Config
	runner Runner

	resourceCacheMu sync.Mutex
	resourceCache   []vmResource
	resourceCacheAt time.Time
}

func NewClient(cfg *Config) (*Client, error) {
	runner, err := NewRunner(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, runner: runner}, nil
}

func (c *Client) Close() error {
	if c.runner != nil {
		return c.runner.Close()
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.runPvesh(ctx, "pvesh unavailable", "get", "/version", "--output-format", "json")
	return err
}

func (c *Client) Open(ctx context.Context, filepath string) (io.ReadCloser, error) {
	return c.runner.Open(ctx, filepath)
}

func (c *Client) Create(ctx context.Context, filepath string) (io.WriteCloser, error) {
	return c.runner.Create(ctx, filepath)
}

func (c *Client) Stat(ctx context.Context, filepath string) (os.FileInfo, error) {
	return c.runner.Stat(ctx, filepath)
}

func (c *Client) Remove(ctx context.Context, filepath string) error {
	return c.runner.Remove(ctx, filepath)
}

func (c *Client) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	return c.runner.Run(ctx, name, args...)
}

func (c *Client) runPvesh(ctx context.Context, errPrefix string, args ...string) (string, error) {
	stdout, stderr, err := c.runner.Run(ctx, "pvesh", args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", errPrefix, err, strings.TrimSpace(stderr))
	}
	return stdout, nil
}
