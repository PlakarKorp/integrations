package common

import (
	"context"
	"fmt"
)

type PoolSession struct {
	Session  *ImapSession
	Selected string
	Bad      bool
}

type ConnectionPool struct {
	connector ImapConnector
	ch        chan *PoolSession
	size      int
}

func NewPool(connector ImapConnector, n int) (*ConnectionPool, error) {
	if n < 1 {
		n = 1
	}
	p := &ConnectionPool{
		connector: connector,
		ch:        make(chan *PoolSession, n),
		size:      n,
	}

	// Probe a single connection so NewPool fails fast on bad credentials or
	// an unreachable server, then seed the pool with empty slots that are
	// connected lazily on first use. This keeps the pool from holding n idle
	// connections open and guarantees the channel always has exactly n slots.
	probe, err := connector.Connect()
	if err != nil {
		return nil, fmt.Errorf("imap pool connect: %w", err)
	}
	_ = probe.Logout()

	for range n {
		p.ch <- &PoolSession{}
	}
	return p, nil
}

func (p *ConnectionPool) Close() {
	for range p.size {
		ps := <-p.ch
		if ps.Session != nil {
			_ = ps.Session.Logout()
		}
	}
}

// WithSession borrows a slot from the pool, ensures it holds a healthy
// connection (reconnecting lazily if needed), runs fn, and always returns a
// slot to the pool afterwards so the pool never shrinks. If fn fails or marks
// the session Bad, the underlying connection is dropped and the slot is
// returned empty for the next caller to reconnect.
func (p *ConnectionPool) WithSession(ctx context.Context, fn func(*PoolSession) error) error {
	var ps *PoolSession
	select {
	case ps = <-p.ch:
	case <-ctx.Done():
		return ctx.Err()
	}

	defer func() {
		if ps.Bad && ps.Session != nil {
			_ = ps.Session.Logout()
			ps.Session = nil
		}
		if ps.Session == nil {
			ps.Selected = ""
			ps.Bad = false
		}
		p.ch <- ps
	}()

	if ps.Session == nil {
		s, err := p.connector.Connect()
		if err != nil {
			return fmt.Errorf("imap pool reconnect: %w", err)
		}
		ps.Session = s
		ps.Selected = ""
		ps.Bad = false
	}

	if err := fn(ps); err != nil {
		ps.Bad = true
		return err
	}
	return nil
}
