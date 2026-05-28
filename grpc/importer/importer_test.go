package importer

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	gconn "github.com/PlakarKorp/integration-grpc"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// fakeServer implements ImporterServer in-process for tests.
// ---------------------------------------------------------------------------

type fakeServer struct {
	UnimplementedImporterServer

	initResp *InitResponse
	initErr  error

	pingErr error

	// records to emit on Import (server-side)
	records []*gconn.Record
	// chunks to emit on Open(record)
	openChunks map[string][][]byte
	openErr    error

	// captured Acks from the client
	acks    []*gconn.Result
	ackDone chan struct{} // closed when Import returns server-side

	closeErr error
}

func (f *fakeServer) Init(ctx context.Context, req *InitRequest) (*InitResponse, error) {
	if f.initErr != nil {
		return nil, f.initErr
	}
	return f.initResp, nil
}

func (f *fakeServer) Ping(ctx context.Context, _ *PingRequest) (*PingResponse, error) {
	return &PingResponse{}, f.pingErr
}

func (f *fakeServer) Close(ctx context.Context, _ *CloseRequest) (*CloseResponse, error) {
	return &CloseResponse{}, f.closeErr
}

func (f *fakeServer) Import(stream grpc.BidiStreamingServer[ImportRequest, ImportResponse]) error {
	if f.ackDone != nil {
		defer close(f.ackDone)
	}
	for _, r := range f.records {
		if err := stream.Send(&ImportResponse{Record: r}); err != nil {
			return err
		}
	}
	if err := stream.Send(&ImportResponse{Finished: true}); err != nil {
		return err
	}

	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if req.Result != nil {
			res, _ := gconn.ResultFromProto(req.Result)
			f.acks = append(f.acks, &gconn.Result{
				Header: gconn.RecordToProto(&res.Record),
			})
		}
	}
}

func (f *fakeServer) Open(req *OpenRequest, stream grpc.ServerStreamingServer[OpenResponse]) error {
	if f.openErr != nil {
		return f.openErr
	}
	chunks := f.openChunks[req.Record.Pathname]
	for _, c := range chunks {
		if err := stream.Send(&OpenResponse{Chunk: c}); err != nil {
			return err
		}
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
	RegisterImporterServer(s, srv)
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

func sampleFI() *gconn.FileInfo {
	return &gconn.FileInfo{
		Name:    "x.txt",
		Size:    3,
		Mode:    0o644,
		ModTime: nil, // ModTime is not nil-safe upstream; test uses helper below
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestImporter_InitAndGetters(t *testing.T) {
	srv := &fakeServer{
		initResp: &InitResponse{
			Origin: "o",
			Type:   "t",
			Root:   "/r",
			Flags:  0,
		},
	}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	imp, err := NewImporter(context.Background(), conn, &connectors.Options{}, "proto", nil)
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}
	if imp.Origin() != "o" || imp.Type() != "t" || imp.Root() != "/r" {
		t.Errorf("getters mismatch: %q %q %q", imp.Origin(), imp.Type(), imp.Root())
	}
	// FLAG_NEEDACK is always OR'd in.
	if imp.Flags() == 0 {
		t.Errorf("expected FLAG_NEEDACK to be set, got 0")
	}
	if err := imp.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
	if err := imp.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestImporter_Init_UnwrapsErrors(t *testing.T) {
	srv := &fakeServer{
		initErr: status.Error(codes.Unavailable, "down"),
	}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	_, err := NewImporter(context.Background(), conn, &connectors.Options{}, "p", nil)
	if err == nil || !strings.Contains(err.Error(), "I/O error") {
		t.Fatalf("expected Unavailable to be wrapped as I/O error, got %v", err)
	}
}

func TestImporter_Init_CanceledMapsToContextCanceled(t *testing.T) {
	srv := &fakeServer{
		initErr: status.Error(codes.Canceled, "ctx"),
	}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	_, err := NewImporter(context.Background(), conn, &connectors.Options{}, "p", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestImporter_ImportStreamsRecordsAndAcks(t *testing.T) {
	rec := &connectors.Record{
		Pathname: "/a.txt",
		FileInfo: objects.FileInfo{
			Lname: "a.txt",
			Lsize: 5,
			Lmode: 0o644,
		},
	}
	pbrec := gconn.RecordToProto(rec)

	srv := &fakeServer{
		initResp:   &InitResponse{Type: "t", Root: "/"},
		records:    []*gconn.Record{pbrec},
		openChunks: map[string][][]byte{"/a.txt": {[]byte("hello")}},
		ackDone:    make(chan struct{}),
	}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	imp, err := NewImporter(context.Background(), conn, &connectors.Options{}, "p", nil)
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}

	records := make(chan *connectors.Record, 4)
	results := make(chan *connectors.Result, 4)

	importDone := make(chan error, 1)
	go func() { importDone <- imp.Import(context.Background(), records, results) }()

	var got *connectors.Record
	select {
	case got = <-records:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for record")
	}
	if got == nil || got.Pathname != "/a.txt" {
		t.Fatalf("unexpected record: %+v", got)
	}

	// Open via LazyReader
	data, err := io.ReadAll(got.Reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("payload mismatch: %q", data)
	}
	_ = got.Reader.Close()

	// ACK
	results <- &connectors.Result{Record: *got}
	close(results)

	if err := <-importDone; err != nil {
		t.Fatalf("Import returned: %v", err)
	}

	// Wait for the server to finish its Import handler so it has actually
	// drained the ack stream — Import returns as soon as the client side
	// CloseSends, but the server may still be receiving.
	select {
	case <-srv.ackDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server import handler to exit")
	}
	if len(srv.acks) != 1 {
		t.Errorf("server saw %d acks, want 1", len(srv.acks))
	}
}

func TestImporter_OpenErrorPropagates(t *testing.T) {
	srv := &fakeServer{
		initResp: &InitResponse{},
		openErr:  status.Error(codes.NotFound, "nope"),
	}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	imp, err := NewImporter(context.Background(), conn, &connectors.Options{}, "p", nil)
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}

	gi := imp.(*Importer)
	rd, err := gi.open(context.Background(), &connectors.Record{Pathname: "/missing"})
	if err != nil {
		t.Fatalf("open returned err immediately: %v", err)
	}
	defer rd.Close()
	if _, err := io.ReadAll(rd); err == nil {
		t.Fatalf("expected read error from server, got nil")
	}
}
