// Reproduces the "backup hangs forever" failure: when an IMAP read stalls
// (server stops responding mid-FETCH, as Gmail does under throttling), a body
// reader blocks indefinitely because no read deadline is set. With enough
// stalled readers to exhaust the importer's connection pool, the whole backup
// deadlocks. Skipped unless IMAP_E2E_ADDR is set.
package importer_test

import (
	"context"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	imapimporter "github.com/PlakarKorp/integrations/imap/importer"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/snapshot"
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
}

func newStallProxy(t *testing.T, backend string, stallAfter int64) *stallProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &stallProxy{backend: backend, stallAfter: stallAfter, ln: ln}
	go p.serve()
	t.Cleanup(func() { ln.Close() })
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
	defer client.Close()
	server, err := net.Dial("tcp", p.backend)
	if err != nil {
		return
	}
	defer server.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	// client -> server, verbatim
	go func() {
		defer wg.Done()
		io.Copy(server, client)
	}()
	// server -> client, until stallAfter bytes then go silent
	go func() {
		defer wg.Done()
		var relayed int64
		buf := make([]byte, 4096)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				if relayed >= p.stallAfter {
					// stall: stop relaying, keep the socket open
					p.stalls.Add(1)
					io.Copy(io.Discard, server)
					return
				}
				w := n
				if relayed+int64(w) > p.stallAfter {
					w = int(p.stallAfter - relayed)
				}
				client.Write(buf[:w])
				relayed += int64(w)
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

	repo := newFsRepo(t)

	imp, err := imapimporter.NewImporter(repo.AppContext(), &connectors.Options{}, "imap", map[string]string{
		"location":   "imap://" + proxy.addr(),
		"username":   "srcuser",
		"password":   "secret",
		"tls":        "no-tls",
		"io_timeout": "3s", // short so the stalled reads fail fast
	})
	require.NoError(t, err)
	defer imp.Close(context.Background())

	src, err := snapshot.NewSource(repo.AppContext(), 0, imp)
	require.NoError(t, err)
	builder, err := snapshot.Create(repo, 0, "", [32]byte{}, &snapshot.BuilderOptions{})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		err := builder.Backup(src)
		done <- err
	}()

	select {
	case err := <-done:
		// We don't require success — a stalled server should surface as an
		// error, not a hang. The point of the test is that we get HERE.
		t.Logf("backup returned (stalls=%d): err=%v", proxy.stalls.Load(), err)
	case <-time.After(45 * time.Second):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("backup HUNG on a stalled IMAP server (stalls=%d)\n--- goroutines ---\n%s", proxy.stalls.Load(), buf[:n])
	}
	_ = builder.Close()
}
