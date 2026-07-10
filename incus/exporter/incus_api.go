/*
 * Copyright (c) 2026 Antoine Dheygers <antoine.dheygers@cryptoweb.fr>
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

package exporter

import (
	"context"
	"fmt"
	"io"

	"github.com/PlakarKorp/integration-incus/internal/conn"
	incus "github.com/lxc/incus/v6/client"
)

type incusSink struct {
	server  incus.InstanceServer
	pool    string
	project string
	target  string
}

func newIncusSink(config map[string]string) (restoreSink, error) {
	// conn.Connect handles the transport (unix socket or HTTPS with
	// client certificate) and the project scoping.
	server, err := conn.Connect(config)
	if err != nil {
		return nil, err
	}
	return &incusSink{
		server:  server,
		pool:    config["pool"],
		project: config["project"],
		target:  config["target"],
	}, nil
}

func (s *incusSink) Ping(ctx context.Context) error {
	_, _, err := s.server.GetServer()
	return err
}

func (s *incusSink) Restore(ctx context.Context, instance string, tarStream io.Reader) error {
	// CreateInstanceFromBackup against a taken name fails only after the
	// full tarball has been uploaded and unpacked server-side. Fail fast
	// with a clear message instead. Best-effort: any pre-check failure
	// other than "exists" falls through to the create call, which reports
	// the real problem if there is one.
	if _, _, err := s.server.GetInstance(instance); err == nil {
		where := ""
		if s.project != "" {
			where = fmt.Sprintf(" in project %q", s.project)
		}
		return fmt.Errorf("instance %q already exists%s, restore to another name or delete it first", instance, where)
	}

	server := s.server
	if s.target != "" {
		// On a cluster, pin the restored instance to the given member;
		// without it the scheduler picks one. UseTarget only influences
		// placement-creating calls, so the pre-check above is unaffected.
		server = server.UseTarget(s.target)
	}
	op, err := server.CreateInstanceFromBackup(incus.InstanceBackupArgs{
		BackupFile: tarStream,
		Name:       instance,
		PoolName:   s.pool,
	})
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}
