package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// fakeServer
// ---------------------------------------------------------------------------

type fakeServer struct {
	UnimplementedStoreServer

	openConfig []byte
	macs       [][]byte
	getChunks  [][]byte
	getErr     error
	mode       int32

	putBytes int64
	putErr   error

	receivedMac    []byte
	receivedType   StorageResource
	receivedChunks [][]byte
}

func (f *fakeServer) Init(ctx context.Context, req *InitRequest) (*InitResponse, error) {
	return &InitResponse{Origin: "o", Type: "t", Root: "/"}, nil
}
func (f *fakeServer) Open(ctx context.Context, _ *OpenRequest) (*OpenResponse, error) {
	return &OpenResponse{Config: f.openConfig}, nil
}
func (f *fakeServer) Create(ctx context.Context, _ *CreateRequest) (*CreateResponse, error) {
	return &CreateResponse{}, nil
}
func (f *fakeServer) Ping(ctx context.Context, _ *PingRequest) (*PingResponse, error) {
	return &PingResponse{}, nil
}
func (f *fakeServer) Close(ctx context.Context, _ *CloseRequest) (*CloseResponse, error) {
	return &CloseResponse{}, nil
}
func (f *fakeServer) Mode(ctx context.Context, _ *ModeRequest) (*ModeResponse, error) {
	return &ModeResponse{Mode: f.mode}, nil
}
func (f *fakeServer) Size(ctx context.Context, _ *SizeRequest) (*SizeResponse, error) {
	return &SizeResponse{Size: 42}, nil
}
func (f *fakeServer) List(ctx context.Context, _ *ListRequest) (*ListResponse, error) {
	return &ListResponse{Macs: f.macs}, nil
}
func (f *fakeServer) Delete(ctx context.Context, _ *DeleteRequest) (*DeleteResponse, error) {
	return &DeleteResponse{}, nil
}

func (f *fakeServer) Put(stream grpc.ClientStreamingServer[PutRequest, PutResponse]) error {
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(req.Mac) > 0 {
			f.receivedMac = append([]byte(nil), req.Mac...)
			f.receivedType = req.Type
		}
		if len(req.Chunk) > 0 {
			f.receivedChunks = append(f.receivedChunks, append([]byte(nil), req.Chunk...))
		}
	}
	if f.putErr != nil {
		return f.putErr
	}
	return stream.SendAndClose(&PutResponse{BytesWritten: f.putBytes})
}

func (f *fakeServer) Get(_ *GetRequest, stream grpc.ServerStreamingServer[GetResponse]) error {
	if f.getErr != nil {
		return f.getErr
	}
	for _, c := range f.getChunks {
		if err := stream.Send(&GetResponse{Chunk: c}); err != nil {
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
	RegisterStoreServer(s, srv)
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

func TestStore_BasicLifecycle(t *testing.T) {
	srv := &fakeServer{openConfig: []byte("cfg")}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	s, err := NewStorage(context.Background(), conn, "p", nil)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}

	if cfg, err := s.Open(context.Background()); err != nil || string(cfg) != "cfg" {
		t.Errorf("Open: %q %v", cfg, err)
	}
	if err := s.Create(context.Background(), []byte("x")); err != nil {
		t.Errorf("Create: %v", err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
	if size, err := s.Size(context.Background()); err != nil || size != 42 {
		t.Errorf("Size: %d %v", size, err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestStore_List_ValidatesMacLength(t *testing.T) {
	srv := &fakeServer{
		macs: [][]byte{make([]byte, 16)},
	}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	s, _ := NewStorage(context.Background(), conn, "p", nil)
	_, err := s.List(context.Background(), storage.StorageResourcePackfile)
	if err == nil {
		t.Fatalf("expected validation error for short MAC")
	}
}

func TestStore_List_RoundTripsMacs(t *testing.T) {
	full := bytes.Repeat([]byte{0x42}, 32)
	srv := &fakeServer{macs: [][]byte{full}}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	s, _ := NewStorage(context.Background(), conn, "p", nil)
	got, err := s.List(context.Background(), storage.StorageResourcePackfile)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0] != objects.MAC(full) {
		t.Errorf("List mismatch: %v", got)
	}
}

func TestStore_PutStreamsChunks(t *testing.T) {
	srv := &fakeServer{putBytes: 11}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	s, _ := NewStorage(context.Background(), conn, "p", nil)

	var mac objects.MAC
	for i := range mac {
		mac[i] = byte(i)
	}
	payload := []byte("hello world")
	n, err := s.Put(context.Background(), storage.StorageResourcePackfile, mac, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != 11 {
		t.Errorf("BytesWritten: %d, want 11", n)
	}
	if !bytes.Equal(srv.receivedMac, mac[:]) {
		t.Errorf("mac mismatch")
	}
	var glued []byte
	for _, c := range srv.receivedChunks {
		glued = append(glued, c...)
	}
	if !bytes.Equal(glued, payload) {
		t.Errorf("payload mismatch: %q vs %q", glued, payload)
	}
}

func TestStore_GetStreamsChunks(t *testing.T) {
	srv := &fakeServer{
		getChunks: [][]byte{[]byte("foo"), []byte("bar"), []byte("baz")},
	}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	s, _ := NewStorage(context.Background(), conn, "p", nil)
	rd, err := s.Get(context.Background(), storage.StorageResourcePackfile, objects.MAC{}, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rd.Close()
	data, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "foobarbaz" {
		t.Errorf("payload mismatch: %q", data)
	}
}

func TestStore_Get_UnavailableMapsToIOError(t *testing.T) {
	srv := &fakeServer{
		getErr: status.Error(codes.Unavailable, "down"),
	}
	conn, cleanup := dialTestServer(t, srv)
	defer cleanup()

	s, _ := NewStorage(context.Background(), conn, "p", nil)
	rd, err := s.Get(context.Background(), storage.StorageResourcePackfile, objects.MAC{}, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rd.Close()

	_, err = io.ReadAll(rd)
	if err == nil {
		t.Fatalf("expected error reading from server that returned Unavailable")
	}
}

func TestSendChunks_PropagatesReadError(t *testing.T) {
	rd := io.NopCloser(&errReader{err: errors.New("disk-on-fire")})
	n, err := SendChunks(rd, func([]byte) error { return nil })
	if n != 0 || err == nil {
		t.Fatalf("expected read error, got n=%d err=%v", n, err)
	}
}

func TestSendChunks_PropagatesSendError(t *testing.T) {
	rd := io.NopCloser(bytes.NewReader([]byte("xxx")))
	sendErr := errors.New("net dead")
	_, err := SendChunks(rd, func([]byte) error { return sendErr })
	if err == nil {
		t.Fatalf("expected send error")
	}
}

func TestReceiveChunks_ReassemblesAcrossChunkBoundaries(t *testing.T) {
	chunks := [][]byte{[]byte("ab"), []byte("cde"), []byte("fghij")}
	i := 0
	rd := ReceiveChunks(func() ([]byte, error) {
		if i >= len(chunks) {
			return nil, io.EOF
		}
		c := chunks[i]
		i++
		return c, nil
	})

	// Read exactly 5 bytes at a time to force the buffer logic to span chunks.
	buf := make([]byte, 5)
	var out bytes.Buffer
	for {
		n, err := rd.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if out.String() != "abcdefghij" {
		t.Errorf("payload mismatch: %q", out.String())
	}
}

type errReader struct{ err error }

func (e *errReader) Read([]byte) (int, error) { return 0, e.err }
