package exporter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	gconn "github.com/PlakarKorp/integration-grpc"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// Fake server
// ---------------------------------------------------------------------------

type fakeServer struct {
	UnimplementedExporterServer

	mu       sync.Mutex
	received []receivedFile

	respond []*connectors.Result
}

type receivedFile struct {
	record  *gconn.Record
	payload []byte
}

func (f *fakeServer) Init(ctx context.Context, req *InitRequest) (*InitResponse, error) {
	return &InitResponse{Origin: "o", Type: "t", Root: "/", Flags: 0}, nil
}
func (f *fakeServer) Ping(ctx context.Context, _ *PingRequest) (*PingResponse, error) {
	return &PingResponse{}, nil
}
func (f *fakeServer) Close(ctx context.Context, _ *CloseRequest) (*CloseResponse, error) {
	return &CloseResponse{}, nil
}

func (f *fakeServer) Export(stream grpc.BidiStreamingServer[ExportRequest, ExportResponse]) error {
	var current *gconn.Record
	var buf bytes.Buffer

	// Pre-queue any responses to send after we see at least one record.
	pending := append([]*connectors.Result(nil), f.respond...)

	flush := func() {
		f.mu.Lock()
		f.received = append(f.received, receivedFile{
			record:  current,
			payload: append([]byte(nil), buf.Bytes()...),
		})
		f.mu.Unlock()
		current = nil
		buf.Reset()
	}

	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		switch p := req.Packet.(type) {
		case *ExportRequest_Record:
			if current != nil {
				flush()
			}
			current = p.Record
			if !p.Record.HasReader {
				flush()
			}
		case *ExportRequest_Chunk:
			if len(p.Chunk) == 0 {
				// terminator chunk (client sends a zero-length chunk
				// after each file; protobuf may unmarshal nil as []byte{})
				flush()
			} else {
				buf.Write(p.Chunk)
			}
		}

		// echo back a result for each completed file, draining the
		// `pending` list (if any was supplied).
		f.mu.Lock()
		for len(pending) > 0 && len(f.received) > 0 {
			r := pending[0]
			pending = pending[1:]
			if err := stream.Send(&ExportResponse{Result: gconn.ResultToProto(r)}); err != nil {
				f.mu.Unlock()
				return err
			}
		}
		f.mu.Unlock()
	}
	if current != nil {
		flush()
	}
	return nil
}

// ---------------------------------------------------------------------------
// scaffolding
// ---------------------------------------------------------------------------

func dialTestServer(t *testing.T, srv *fakeServer) (*grpc.ClientConn, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	RegisterExporterServer(s, srv)
	go func() { _ = s.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough://bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		s.Stop()
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestExporter_InitAndGetters(t *testing.T) {
	conn, cleanup := dialTestServer(t, &fakeServer{})
	defer cleanup()

	exp, err := NewExporter(context.Background(), conn, &connectors.Options{}, "p", nil)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if exp.Origin() != "o" || exp.Type() != "t" || exp.Root() != "/" {
		t.Errorf("getters: %q %q %q", exp.Origin(), exp.Type(), exp.Root())
	}
}

func TestExporter_SendsRecordAndChunks(t *testing.T) {
	srv := &fakeServer{
		respond: []*connectors.Result{
			{Record: connectors.Record{Pathname: "/a.txt"}},
		},
	}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	exp, err := NewExporter(context.Background(), conn, &connectors.Options{}, "p", nil)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	payload := bytes.Repeat([]byte("ab"), 100)
	rec := &connectors.Record{
		Pathname: "/a.txt",
		FileInfo: objects.FileInfo{
			Lname: "a.txt",
			Lsize: int64(len(payload)),
			Lmode: 0o644,
		},
		Reader: io.NopCloser(bytes.NewReader(payload)),
	}

	records := make(chan *connectors.Record, 1)
	results := make(chan *connectors.Result, 1)
	records <- rec
	close(records)

	done := make(chan error, 1)
	go func() { done <- exp.Export(context.Background(), records, results) }()

	// Drain results.
	gotResult := false
	for r := range results {
		if r == nil {
			t.Fatalf("nil result")
		}
		gotResult = true
	}
	if !gotResult {
		t.Errorf("expected at least one result from server")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for Export")
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.received) != 1 {
		t.Fatalf("server saw %d files, want 1", len(srv.received))
	}
	if !bytes.Equal(srv.received[0].payload, payload) {
		t.Errorf("payload mismatch: got %d bytes, want %d", len(srv.received[0].payload), len(payload))
	}
}

func TestExporter_DirectoryRecordSkipsChunks(t *testing.T) {
	srv := &fakeServer{}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	exp, err := NewExporter(context.Background(), conn, &connectors.Options{}, "p", nil)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	rec := &connectors.Record{
		Pathname: "/d",
		FileInfo: objects.FileInfo{
			Lname: "d",
			Lmode: 0o755 | 0x80000000, // dir bit
		},
	}

	records := make(chan *connectors.Record, 1)
	results := make(chan *connectors.Result, 1)
	records <- rec
	close(records)

	done := make(chan error, 1)
	go func() { done <- exp.Export(context.Background(), records, results) }()

	for range results {
	}
	<-done

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.received) != 1 || len(srv.received[0].payload) != 0 {
		t.Errorf("expected one empty-payload record, got %+v", srv.received)
	}
}
