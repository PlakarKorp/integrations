package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	gconn "github.com/PlakarKorp/integration-grpc/v2"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Importer struct {
	client ImporterClient

	cookie string
	typ    string
	origin string
	root   string
	flags  location.Flags
}

func unwrap(err error) error {
	if err == nil {
		return nil
	}

	status, ok := status.FromError(err)
	if !ok {
		return err
	}

	switch status.Code() {
	case codes.Canceled:
		return context.Canceled
	default:
		return fmt.Errorf("%s", status.Message())
	}
}

func NewImporter(ctx context.Context, client grpc.ClientConnInterface, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	importer := &Importer{
		client: NewImporterClient(client),
	}

	initReq := InitRequest{
		Options: &Options{
			Hostname:       opts.Hostname,
			Os:             opts.OperatingSystem,
			Arch:           opts.Architecture,
			Cwd:            opts.CWD,
			Maxconcurrency: int64(opts.MaxConcurrency),
			Excludes:       opts.Excludes,
		},
		Proto:  proto,
		Config: config,
	}

	res, err := importer.client.Init(ctx, &initReq)
	if err != nil {
		return nil, unwrap(err)
	}

	importer.typ = res.Type
	importer.origin = res.Origin
	importer.root = res.Root

	// We always need acknowledgments and forward them to the
	// other side.  It'll be go-kloset-sdk job to not propagate
	// them eventually.
	importer.flags = location.Flags(res.Flags) | location.FLAG_NEEDACK

	return importer, nil
}

func (g *Importer) Origin() string        { return g.origin }
func (g *Importer) Type() string          { return g.typ }
func (g *Importer) Root() string          { return g.root }
func (g *Importer) Flags() location.Flags { return g.flags }

func (g *Importer) Ping(ctx context.Context) error {
	_, err := g.client.Ping(ctx, &PingRequest{})
	return unwrap(err)
}

func (g *Importer) Close(ctx context.Context) error {
	_, err := g.client.Close(ctx, &CloseRequest{})
	return unwrap(err)
}

type streamReader struct {
	ctx    context.Context
	cancel func()
	stream grpc.ServerStreamingClient[OpenResponse]
	buf    bytes.Buffer
}

func (g *Importer) open(parentctx context.Context, record *connectors.Record) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(parentctx)

	stream, err := g.client.Open(ctx, &OpenRequest{
		Record: &gconn.Record{
			Pathname:  record.Pathname,
			IsXattr:   record.IsXattr,
			XattrName: record.XattrName,
			XattrType: gconn.ExtendedAttributeType(record.XattrType),
			Target:    record.Target,
		},
	})
	if err != nil {
		cancel() // possibly not needed, but makes LSP happy
		return nil, err
	}

	return &streamReader{
		ctx:    ctx,
		cancel: cancel,
		stream: stream,
	}, nil
}

func (s *streamReader) Read(p []byte) (n int, err error) {
	if s.buf.Len() != 0 {
		n, err = s.buf.Read(p)
		if n > 0 || err != nil {
			return n, err
		}
	}

	fileResponse, err := s.stream.Recv()
	if err != nil {
		if err == io.EOF {
			return 0, io.EOF
		}
		return 0, fmt.Errorf("failed to receive file data: %w", unwrap(err))
	}
	if fileResponse.GetChunk() != nil {
		s.buf.Write(fileResponse.GetChunk())
		n, err = s.buf.Read(p)
		if n > 0 || err != nil {
			return n, err
		}
	}
	return 0, fmt.Errorf("unexpected response: %v", fileResponse)
}

// Closing of the actual reader is left to the caller, close is not to be
// forwarded over grpc.
// We still drain this in order to avoid a misuse of the API (where someone
// would request less than what the server is sending), which leads to leaks.
func (s *streamReader) Close() error {
	s.cancel()

	for {
		_, err := s.stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
	}
}

func (g *Importer) receiveRecords(ctx context.Context, stream grpc.BidiStreamingClient[ImportRequest, ImportResponse], records chan<- *connectors.Record) error {
	defer close(records)

	for {
		res, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return err
		}

		if res != nil && res.Finished {
			return nil
		}

		if res == nil || res.Record == nil {
			return fmt.Errorf("expected a record")
		}

		record, err := gconn.RecordFromProto(res.Record)
		if err != nil {
			return err
		}

		record.Reader = connectors.NewLazyReader(func() (io.ReadCloser, error) {
			return g.open(ctx, record)
		})

		records <- record
	}
}

func (g *Importer) sendResults(stream grpc.BidiStreamingClient[ImportRequest, ImportResponse], results <-chan *connectors.Result) error {
	for result := range results {
		hdr := ImportRequest{
			Result: gconn.ResultToProto(result),
		}
		if err := stream.Send(&hdr); err != nil {
			return err
		}
	}

	return stream.CloseSend()
}

func (g *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	stream, err := g.client.Import(ctx)
	if err != nil {
		return err
	}

	var wg errgroup.Group

	wg.Go(func() error {
		return g.receiveRecords(ctx, stream, records)
	})

	wg.Go(func() error {
		return g.sendResults(stream, results)
	})

	return wg.Wait()
}
