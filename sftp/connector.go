/*
 * Copyright (c) 2025 Gilles Chehade <gilles@poolp.org>
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

package sftp

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/exclude"
	"github.com/PlakarKorp/kloset/location"
	"github.com/pkg/sftp"
	"golang.org/x/sync/singleflight"
)

type Sftp struct {
	opts *connectors.Options

	client   *sftp.Client
	endpoint *url.URL

	rootDir  string
	realpath string
	excludes *exclude.RuleSet

	setOwner bool

	hlCreate singleflight.Group // key -> ensures canonical exists, returns canonical abs path
	hlCanon  sync.Map           // key -> canonical abs path string
	hlMu     sync.Map           // key -> *sync.Mutex (serialize os.Link per key)

	packfiles Buckets
	states    Buckets
}

func New(ctx context.Context, opts *connectors.Options, name string, config map[string]string, kind string) (*Sftp, error) {
	sftp := Sftp{
		opts: opts,
	}

	var port string
	if tmp, ok := config["port"]; ok {
		port = tmp
	}
	var root string
	if tmp, ok := config["root"]; ok {
		root = tmp
	}

	parsed, err := url.Parse(config["location"])
	if err != nil {
		return nil, err
	}

	rootDir := parsed.Path
	if root != "" {
		rootDir = root
	}
	if rootDir == "" {
		rootDir = "/"
	}
	sftp.rootDir = rootDir

	if parsed.Port() == "" && port != "" {
		parsed.Host = fmt.Sprintf("%s:%s", parsed.Host, port)
	}

	sftp.endpoint = parsed

	if opts != nil {
		excludes := exclude.NewRuleSet()
		if err := excludes.AddRulesFromArray(opts.Excludes); err != nil {
			return nil, fmt.Errorf("failed to setup exclude rules: %w", err)
		}
		sftp.excludes = excludes
	}

	switch kind {
	case "exporter":
		if tmp, ok := config["set_owner"]; ok {
			sftp.setOwner, err = strconv.ParseBool(tmp)
			if err != nil {
				return nil, fmt.Errorf("set_owner: bad value: %w", err)
			}
		}
	}

	sftp.client, err = connect(sftp.endpoint, config)
	if err != nil {
		return nil, err
	}

	if kind == "importer" {
		sftp.realpath, err = sftp.realpathFollow(rootDir)
		if err != nil {
			return nil, err
		}
	}

	return &sftp, nil
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	return New(ctx, opts, name, config, "importer")
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	return New(ctx, opts, name, config, "exporter")
}

func NewStore(ctx context.Context, name string, config map[string]string) (storage.Store, error) {
	return New(ctx, nil, name, config, "storage")
}

func (s *Sftp) Type() string          { return "sftp" }
func (s *Sftp) Origin() string        { return s.endpoint.Host }
func (s *Sftp) Root() string          { return s.rootDir }
func (s *Sftp) Flags() location.Flags { return 0 }

func (s *Sftp) Ping(ctx context.Context) error {
	_, err := s.client.Lstat(s.rootDir)
	return err
}

func (s *Sftp) Close(ctx context.Context) error {
	return s.client.Close()
}
