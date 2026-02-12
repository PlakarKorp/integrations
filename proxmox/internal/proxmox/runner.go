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
	"io"
	"os"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, string, error)
	Stream(ctx context.Context, name string, args ...string) (*CommandStream, error)
	Open(ctx context.Context, filepath string) (io.ReadCloser, error)
	Create(ctx context.Context, filepath string) (io.WriteCloser, error)
	Stat(ctx context.Context, filepath string) (os.FileInfo, error)
	Remove(ctx context.Context, filepath string) error
	Close() error
}

type CommandStream struct {
	Stdout io.Reader
	Stderr io.Reader
	finish func() error
	abort  func() error
}

func (s *CommandStream) Finish() error {
	if s == nil || s.finish == nil {
		return nil
	}
	return s.finish()
}

func (s *CommandStream) Abort() error {
	if s == nil || s.abort == nil {
		return nil
	}
	return s.abort()
}

func NewRunner(cfg *Config) (Runner, error) {
	if cfg.Mode == ModeLocal {
		return &LocalRunner{}, nil
	}
	return NewSSHRunner(cfg)
}
