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

	incus "github.com/lxc/incus/v6/client"
)

type incusSink struct {
	server incus.InstanceServer
	pool   string
}

func newIncusSink(socket, pool string) (restoreSink, error) {
	server, err := incus.ConnectIncusUnix(socket, nil)
	if err != nil {
		return nil, fmt.Errorf("incus: connect %s: %w", socket, err)
	}
	return &incusSink{server: server, pool: pool}, nil
}

func (s *incusSink) Ping(ctx context.Context) error {
	_, _, err := s.server.GetServer()
	return err
}

func (s *incusSink) Restore(ctx context.Context, instance string, tarStream io.Reader) error {
	op, err := s.server.CreateInstanceFromBackup(incus.InstanceBackupArgs{
		BackupFile: tarStream,
		Name:       instance,
		PoolName:   s.pool,
	})
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}
