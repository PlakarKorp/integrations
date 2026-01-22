package exporter

import (
	"context"
	"errors"
	"fmt"
	"io"

	gconn "github.com/PlakarKorp/integration-grpc/v2"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Exporter struct {
	client ExporterClient

	cookie string
	root   string
	typ    string
	origin string
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

func NewExporter(ctx context.Context, client grpc.ClientConnInterface, opts *connectors.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	exporter := &Exporter{
		client: NewExporterClient(client),
	}

	res, err := exporter.client.Init(ctx, &InitRequest{
		Options: &Options{
			Maxconcurrency: int64(opts.MaxConcurrency),
		},
		Proto:  proto,
		Config: config,
	})
	if err != nil {
		return nil, unwrap(err)
	}

	exporter.root = res.Root
	exporter.typ = res.Type
	exporter.origin = res.Origin
	exporter.flags = location.Flags(res.Flags)

	return exporter, nil
}

func (g *Exporter) Origin() string        { return g.origin }
func (g *Exporter) Type() string          { return g.typ }
func (g *Exporter) Root() string          { return g.root }
func (g *Exporter) Flags() location.Flags { return g.flags }

func (g *Exporter) Ping(ctx context.Context) error {
	_, err := g.client.Ping(ctx, &PingRequest{})
	return unwrap(err)
}

func (g *Exporter) Close(ctx context.Context) error {
	_, err := g.client.Close(ctx, &CloseRequest{})
	return unwrap(err)
}

func (g *Exporter) transmitRecords(stream grpc.BidiStreamingClient[ExportRequest, ExportResponse], records <-chan *connectors.Record) error {
	defer stream.CloseSend()

	for record := range records {
		hdr := ExportRequest{
			Packet: &ExportRequest_Record{
				Record: gconn.RecordToProto(record),
			},
		}
		if err := stream.Send(&hdr); err != nil {
			return err
		}

		if !hdr.GetRecord().HasReader {
			continue
		}

		sendData := func(buf []byte) error {
			return stream.Send(&ExportRequest{
				Packet: &ExportRequest_Chunk{
					Chunk: buf,
				},
			})
		}

		buf := make([]byte, 1024*1024)
		for {
			n, err := record.Reader.Read(buf)
			if n != 0 {
				if err := sendData(buf[:n]); err != nil {
					return err
				}
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
		}

		if err := sendData(nil); err != nil {
			return err
		}
	}

	return nil
}

func (g *Exporter) receiveResults(stream grpc.BidiStreamingClient[ExportRequest, ExportResponse], results chan<- *connectors.Result) error {
	defer close(results)

	for {
		res, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return err
		}

		result, err := gconn.ResultFromProto(res.Result)
		if err != nil {
			return err
		}

		results <- result
	}
}

func (g *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	stream, err := g.client.Export(ctx)
	if err != nil {
		return err
	}

	var wg errgroup.Group

	wg.Go(func() error {
		return g.transmitRecords(stream, records)
	})

	wg.Go(func() error {
		return g.receiveResults(stream, results)
	})

	return wg.Wait()
}
