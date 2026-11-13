// Reproduces the "backup hangs forever" failure: when an IMAP read stalls
// (server stops responding mid-FETCH, as Gmail does under throttling), a body
// reader blocks indefinitely because no read deadline is set. With enough
// stalled readers to exhaust the importer's connection pool, the whole backup
// deadlocks. Skipped unless IMAP_E2E_ADDR is set.
package importer_test

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stallProxy forwards traffic to the real server until it has relayed
// stallAfter bytes from the server to a client, then goes silent on that
// connection (simulating a throttled/hung server) while leaving it open.
type stallProxy struct {
	backend    string
	stallAfter int64
	ln         net.Listener
	stalls     atomic.Int64

	// stallConns limits how many connections will stall. -1 means "every
	// connection"; a positive N stalls only the first N connections (by accept
	// order) and lets the rest through transparently, modelling a transient
	// failure that a retry on a fresh connection recovers from.
	stallConns int64
	connSeq    atomic.Int64
}

func newStallProxy(t *testing.T, backend string, stallAfter int64) *stallProxy {
	return newStallProxyN(t, backend, stallAfter, -1)
}

func newStallProxyN(t *testing.T, backend string, stallAfter int64, stallConns int64) *stallProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &stallProxy{backend: backend, stallAfter: stallAfter, ln: ln, stallConns: stallConns}
	go p.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *stallProxy) addr() string { return p.ln.Addr().String() }

func (p *stallProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

func (p *stallProxy) handle(client net.Conn) {
	defer func() { _ = client.Close() }()
	server, err := net.Dial("tcp", p.backend)
	if err != nil {
		return
	}
	defer func() { _ = server.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)
	// client -> server, verbatim
	go func() {
		defer wg.Done()
		_, _ = io.Copy(server, client)
	}()
	// server -> client, until stallAfter bytes then (maybe) go silent. The
	// stall budget is consumed only when a connection actually reaches the
	// threshold (i.e. carries a large body), so small metadata connections are
	// never counted and never stall.
	go func() {
		defer wg.Done()
		var relayed int64
		decided := false // whether this connection's stall decision is made
		buf := make([]byte, 4096)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				if !decided && relayed >= p.stallAfter {
					decided = true
					if p.stallConns < 0 || p.connSeq.Add(1) <= p.stallConns {
						p.stalls.Add(1)
						_, _ = io.Copy(io.Discard, server) // stall: keep socket open, relay nothing
						return
					}
				}
				_, _ = client.Write(buf[:n])
				relayed += int64(n)
			}
			if err != nil {
				return
			}
		}
	}()
	wg.Wait()
}

func TestBackupStallDoesNotHangForever(t *testing.T) {
	a := addr(t)
	seedLarge(t, a, "srcuser", 8, 256*1024) // 8 messages of 256 KiB each

	// Stall server->client after ~64 KiB, i.e. partway through the body
	// FETCH phase, so several body reads hang mid-transfer.
	proxy := newStallProxy(t, a, 64*1024)

	imp := newImporter(t, map[string]string{
		"location":   "imap://" + proxy.addr(),
		"username":   "srcuser",
		"password":   "secret",
		"tls":        "no-tls",
		"io_timeout": "3s", // short so the stalled reads fail fast
	})

	// More concurrent readers than the importer's connection pool, so stalled
	// reads can exhaust it. We don't require success — a stalled server should
	// surface as errors, not a hang. The point of the test is that runImport
	// returns before its timeout.
	run := runImport(t, imp, 8, 45*time.Second)
	t.Logf("import returned (stalls=%d): err=%v, error records=%d, read errors=%d",
		proxy.stalls.Load(), run.err, len(run.recErrs), len(run.readErrs))
}

// TestBackupTransientStallRetries verifies that when only the first couple of
// body-fetch connections stall (a transient throttle), the importer's retry on
// a fresh connection recovers them, so every message body is read in full
// rather than failing.
func TestBackupTransientStallRetries(t *testing.T) {
	a := addr(t)
	const n = 6
	const size = 256 * 1024
	seedLarge(t, a, "srcuser", n, size)

	// Only the first 2 large-body connections stall; their retries land on
	// fresh, healthy connections.
	proxy := newStallProxyN(t, a, 64*1024, 2)

	imp := newImporter(t, map[string]string{
		"location":   "imap://" + proxy.addr(),
		"username":   "srcuser",
		"password":   "secret",
		"tls":        "no-tls",
		"io_timeout": "2s",
	})

	run := runImport(t, imp, 8, 45*time.Second)
	requireCleanRun(t, run)

	t.Logf("stalls observed=%d, bodies read=%d/%d", proxy.stalls.Load(), len(run.bodies), n)
	require.GreaterOrEqual(t, proxy.stalls.Load(), int64(1), "expected at least one stall to be exercised")
	require.Len(t, run.bodies, n, "retry should have recovered every message despite transient stalls")
	for p, body := range run.bodies {
		require.Greater(t, len(body), size, "body of %q is truncated", p)
	}
}
