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
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
)

type LocalRunner struct{}

func (r *LocalRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	return stdout.String(), stderr.String(), cmd.Run()
}

func (r *LocalRunner) Stream(ctx context.Context, name string, args ...string) (*CommandStream, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &CommandStream{
		Stdout: stdout,
		Stderr: stderr,
		finish: cmd.Wait,
		abort: func() error {
			if cmd.Process == nil {
				return nil
			}
			return cmd.Process.Kill()
		},
	}, nil
}

func (r *LocalRunner) Open(ctx context.Context, filepath string) (io.ReadCloser, error) {
	return os.Open(filepath)
}

func (r *LocalRunner) Create(ctx context.Context, filepath string) (io.WriteCloser, error) {
	return os.Create(filepath)
}

func (r *LocalRunner) Stat(ctx context.Context, filepath string) (os.FileInfo, error) {
	return os.Stat(filepath)
}

func (r *LocalRunner) Remove(ctx context.Context, filepath string) error {
	return os.Remove(filepath)
}

func (r *LocalRunner) Close() error {
	return nil
}
