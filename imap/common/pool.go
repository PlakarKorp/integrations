package common

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrOperationTimeout is returned by WithSession when an operation exceeds the
// watchdog budget and is abandoned. It is retryable: a fresh connection may
// succeed.
var ErrOperationTimeout = errors.New("imap operation timed out")

// opWatchdogTimeout bounds a single pooled IMAP operation. go-imap performs
// literal transfers through an internal channel handshake between its reader
// goroutine and the caller; if the server stalls mid-transfer, both park on Go
// channels (not on the socket), so a socket read deadline alone cannot break
// the deadlock. The watchdog force-closes the connection, which unblocks the
// reader goroutine and lets the operation return an error.
const opWatchdogTimeout = 3 * time.Minute

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

	// Returned exactly once: either the original slot (operation completed) or a
	// fresh empty slot (operation was abandoned because it stalled).
	returned := false
	giveBack := func(s *PoolSession) {
		if returned {
			return
		}
		returned = true
		p.ch <- s
	}
	defer func() {
		// If the slot was already abandoned (stalled op), do not touch ps — the
		// leaked goroutine still owns it.
		if returned {
			return
		}
		// Normal completion path: recycle the slot, dropping a poisoned
		// connection so the next caller reconnects.
		if ps.Bad && ps.Session != nil {
			ps.Session.ForceClose()
			ps.Session = nil
		}
		if ps.Session == nil {
			ps.Selected = ""
			ps.Bad = false
		}
		giveBack(ps)
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

	budget := p.opBudget()
	done := make(chan error, 1)
	go func() { done <- fn(ps) }()

	timer := time.NewTimer(budget)
	defer timer.Stop()

	select {
	case err := <-done:
		if err != nil {
			ps.Bad = true
		}
		return err

	case <-timer.C:
		return p.abandon(ps, &returned, fmt.Errorf("%w after %s", ErrOperationTimeout, budget))
	case <-ctx.Done():
		return p.abandon(ps, &returned, ctx.Err())
	}
}

// abandon detaches a stalled operation: go-imap can wedge in an internal
// channel handshake from which neither a read deadline nor closing the socket
// can reliably recover it, so we do NOT wait for fn to return. We best-effort
// force-close the socket (to release the file descriptor), hand a FRESH empty
// slot back to the pool, and let the wedged goroutine die on its own. This
// keeps one stalled mailbox from deadlocking the whole backup.
func (p *ConnectionPool) abandon(ps *PoolSession, returned *bool, cause error) error {
	// Best-effort: drop the socket to release the file descriptor. We read
	// ps.Session once; the wedged goroutine keeps using ps, so we must not
	// mutate it here.
	if sess := ps.Session; sess != nil {
		sess.ForceClose()
	}
	if !*returned {
		*returned = true
		p.ch <- &PoolSession{} // replacement slot, connected lazily on next use
	}
	return cause
}

func (p *ConnectionPool) opBudget() time.Duration {
	budget := p.connector.IOTimeout
	if budget <= 0 {
		return opWatchdogTimeout
	}
	// Allow the operation a little longer than a single idle read so the socket
	// deadline can fire first when the stall happens during an active read.
	return budget * 2
}
