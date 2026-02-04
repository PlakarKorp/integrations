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
	p := &ConnectionPool{
		connector: connector,
		ch:        make(chan *PoolSession, n),
		size:      n,
	}

	for i := range n {
		s, err := connector.Connect()
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("imap pool connect %d/%d: %w", i+1, n, err)
		}
		p.ch <- &PoolSession{Session: s, Selected: ""} // nothing selected yet
	}
	return p, nil
}

func (p *ConnectionPool) Close() {
	for {
		select {
		case ps := <-p.ch:
			_ = ps.Session.Logout()
		default:
			return
		}
	}
}

func (p *ConnectionPool) WithSession(ctx context.Context, fn func(*PoolSession) error) error {
	var ps *PoolSession
	select {
	case ps = <-p.ch:
	case <-ctx.Done():
		return ctx.Err()
	}

	ok := true
	defer func() {
		if ok {
			p.ch <- ps
			return
		}
		_ = ps.Session.Logout()
		if ns, err := p.connector.Connect(); err == nil {
			p.ch <- &PoolSession{Session: ns, Selected: ""}
		}
	}()

	err := fn(ps)
	if err != nil {
		// Treat any error as “maybe poisoned”.
		// (If you want, only poison on net.Error/EOF.)
		ok = false
		return err
	}
	return nil
}
