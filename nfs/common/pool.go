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

package common

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/vmware/go-nfs-client/nfs"
)

// Pool spreads operations over several mounts of the same export. Each Conn
// serializes its own RPCs, so a single connection runs one call at a time;
// with a pool, concurrent callers mostly land on different connections and
// proceed in parallel.
//
// Connections past the first are dialed on first use. If one cannot be
// dialed (a server limiting mounts per client, say), its slot falls back to
// the first connection rather than failing the operation.
type Pool struct {
	cfg   *Config
	slots []poolSlot
	next  atomic.Uint32
}

type poolSlot struct {
	once sync.Once
	conn *Conn
}

// NewPool dials the first connection right away, so a bad configuration or an
// unreachable server is reported at once, and leaves the rest for later.
func NewPool(cfg *Config, size int) (*Pool, error) {
	if size < 1 {
		size = 1
	}
	first, err := Connect(cfg)
	if err != nil {
		return nil, err
	}
	p := &Pool{cfg: cfg, slots: make([]poolSlot, size)}
	p.slots[0].once.Do(func() { p.slots[0].conn = first })
	return p, nil
}

func (p *Pool) conn() *Conn {
	i := int(p.next.Add(1)-1) % len(p.slots)
	slot := &p.slots[i]
	slot.once.Do(func() {
		conn, err := Connect(p.cfg)
		if err != nil {
			conn = p.slots[0].conn
		}
		slot.conn = conn
	})
	return slot.conn
}

func (p *Pool) Lookup(path string) (*nfs.Fattr, []byte, error) {
	return p.conn().Lookup(path)
}

func (p *Pool) ReadDirPlus(dir string) ([]*nfs.EntryPlus, error) {
	return p.conn().ReadDirPlus(dir)
}

func (p *Pool) Mkdir(path string, perm os.FileMode) error {
	return p.conn().Mkdir(path, perm)
}

func (p *Pool) OpenReader(path string) (io.ReadCloser, error) {
	return p.conn().OpenReader(path)
}

func (p *Pool) OpenReaderRange(path string, offset, length int64) (io.ReadCloser, error) {
	return p.conn().OpenReaderRange(path, offset, length)
}

func (p *Pool) WriteFile(path string, perm os.FileMode, r io.Reader) error {
	return p.conn().WriteFile(path, perm, r)
}

func (p *Pool) Rename(oldPath, newPath string) error {
	return p.conn().Rename(oldPath, newPath)
}

func (p *Pool) Remove(path string) error {
	return p.conn().Remove(path)
}

// Close closes every connection that was dialed, each exactly once.
func (p *Pool) Close() error {
	var errs []error
	seen := map[*Conn]bool{}
	for i := range p.slots {
		slot := &p.slots[i]
		// Settle slots never used, so a racing conn() can't dial after Close.
		slot.once.Do(func() { slot.conn = p.slots[0].conn })
		if slot.conn == nil || seen[slot.conn] {
			continue
		}
		seen[slot.conn] = true
		if err := slot.conn.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
