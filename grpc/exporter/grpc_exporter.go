package exporter

import (
	"context"
	"fmt"
	"io"

	grpc_exporter "github.com/PlakarKorp/integration-grpc/exporter/pkg"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/exporter"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	// google being google I guess.  No idea why this is actually
	// required, but otherwise it breaks the workspace setup
	// c.f. https://github.com/googleapis/go-genproto/issues/1015
	_ "google.golang.org/genproto/protobuf/ptype"

	"google.golang.org/protobuf/types/known/timestamppb"
)

type GrpcExporter struct {
	GrpcClient grpc_exporter.ExporterClient
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

func NewExporter(ctx context.Context, client grpc.ClientConnInterface, opts *exporter.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	exporter := &GrpcExporter{
		GrpcClient: grpc_exporter.NewExporterClient(client),
	}

	_, err := exporter.GrpcClient.Init(ctx, &grpc_exporter.InitRequest{
		Options: &grpc_exporter.Options{
			Maxconcurrency: int64(opts.MaxConcurrency),
		},
		Proto:  proto,
		Config: config,
	})
	if err != nil {
		return nil, unwrap(err)
	}

	return exporter, nil
}

func (g *GrpcExporter) Close(ctx context.Context) error {
	_, err := g.GrpcClient.Close(ctx, &grpc_exporter.CloseRequest{})
	return unwrap(err)
}

func (g *GrpcExporter) CreateDirectory(ctx context.Context, pathname string) error {
	_, err := g.GrpcClient.CreateDirectory(ctx, &grpc_exporter.CreateDirectoryRequest{Pathname: pathname})
	return unwrap(err)
}

func (g *GrpcExporter) Root(ctx context.Context) (string, error) {
	info, err := g.GrpcClient.Root(ctx, &grpc_exporter.RootRequest{})
	if err != nil {
		return "", unwrap(err)
	}
	return info.RootPath, nil
}

func (g *GrpcExporter) SetPermissions(ctx context.Context, pathname string, fileinfo *objects.FileInfo) error {
	_, err := g.GrpcClient.SetPermissions(ctx, &grpc_exporter.SetPermissionsRequest{
		Pathname: pathname,
		FileInfo: &grpc_exporter.FileInfo{
			Name:      fileinfo.Lname,
			Mode:      uint32(fileinfo.Lmode),
			ModTime:   timestamppb.New(fileinfo.LmodTime),
			Dev:       fileinfo.Ldev,
			Ino:       fileinfo.Lino,
			Uid:       fileinfo.Luid,
			Gid:       fileinfo.Lgid,
			Nlink:     uint32(fileinfo.Lnlink),
			Username:  fileinfo.Lusername,
			Groupname: fileinfo.Lgroupname,
			Flags:     fileinfo.Flags,
		},
	})
	return unwrap(err)
}

func (g *GrpcExporter) CreateLink(ctx context.Context, oldname string, newname string, ltype exporter.LinkType) error {
	_, err := g.GrpcClient.CreateLink(ctx, &grpc_exporter.CreateLinkRequest{
		Oldname: oldname,
		Newname: newname,
		Ltype:   uint32(ltype),
	})

	return unwrap(err)
}

func (g *GrpcExporter) StoreFile(ctx context.Context, pathname string, fp io.Reader, size int64) error {
	stream, err := g.GrpcClient.StoreFile(ctx)
	if err != nil {
		return unwrap(err)
	}

	if err := stream.Send(&grpc_exporter.StoreFileRequest{
		Type: &grpc_exporter.StoreFileRequest_Header{
			Header: &grpc_exporter.Header{
				Pathname: pathname,
				Size:     uint64(size),
			},
		},
	}); err != nil {
		return unwrap(err)
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := fp.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if err := stream.Send(&grpc_exporter.StoreFileRequest{
			Type: &grpc_exporter.StoreFileRequest_Data{
				Data: &grpc_exporter.Data{
					Chunk: buf[:n],
				},
			},
		}); err != nil {
			return unwrap(err)
		}
	}

	_, err = stream.CloseAndRecv()
	return unwrap(err)
}
