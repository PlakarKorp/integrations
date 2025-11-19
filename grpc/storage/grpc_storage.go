/*
 * Copyright (c) 2021 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpc_storage "github.com/PlakarKorp/integration-grpc/storage/pkg"
)

type GrpcStorage struct {
	GrpcClient grpc_storage.StoreClient
	cookie     string
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

func NewStorage(ctx context.Context, client grpc.ClientConnInterface, proto string, config map[string]string) (storage.Store, error) {
	storage := &GrpcStorage{
		GrpcClient: grpc_storage.NewStoreClient(client),
	}

	res, err := storage.GrpcClient.Init(ctx, &grpc_storage.InitRequest{
		Proto:  proto,
		Config: config,
	})
	if err != nil {
		return nil, unwrap(err)
	}

	storage.cookie = res.Cookie
	return storage, nil
}

func (s *GrpcStorage) Create(ctx context.Context, config []byte) error {
	_, err := s.GrpcClient.Create(ctx, &grpc_storage.CreateRequest{
		Config: config,
		Cookie: s.cookie,
	})
	if err != nil {
		return unwrap(err)
	}
	return nil
}

func (s *GrpcStorage) Open(ctx context.Context) ([]byte, error) {
	resp, err := s.GrpcClient.Open(ctx, &grpc_storage.OpenRequest{Cookie: s.cookie})
	if err != nil {
		return nil, unwrap(err)
	}
	return resp.Config, nil
}

func (s *GrpcStorage) Location(ctx context.Context) (string, error) {
	resp, err := s.GrpcClient.GetLocation(ctx, &grpc_storage.GetLocationRequest{
		Cookie: s.cookie,
	})
	if err != nil {
		return "", unwrap(err)
	}
	return resp.Location, nil
}

func (s *GrpcStorage) Mode(ctx context.Context) (storage.Mode, error) {
	resp, err := s.GrpcClient.GetMode(ctx, &grpc_storage.GetModeRequest{
		Cookie: s.cookie,
	})
	if err != nil {
		return storage.Mode(0), unwrap(err)
	}
	return storage.Mode(resp.Mode), nil
}

func (s *GrpcStorage) Size(ctx context.Context) (int64, error) {
	resp, err := s.GrpcClient.GetSize(ctx, &grpc_storage.GetSizeRequest{
		Cookie: s.cookie,
	})
	if err != nil {
		return -1, unwrap(err)
	}
	return resp.Size, nil
}

func SendChunks(rd io.ReadCloser, chunkSendFn func(chunk []byte) error) (int64, error) {
	// 1MB buffer, the underlying grpc transport limits us to 4MB.
	buffer := make([]byte, 1024*1024)
	var totalBytes int64

	defer rd.Close()
	for {
		n, err := rd.Read(buffer)
		if n > 0 {
			if err := chunkSendFn(buffer[:n]); err != nil {
				return totalBytes, fmt.Errorf("failed to send chunk: %w", unwrap(err))
			}
			totalBytes += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return totalBytes, fmt.Errorf("failed to read: %w", unwrap(err))
		}
	}
	return totalBytes, nil
}

type grpcChunkReader struct {
	streamRecv func() ([]byte, error)
	buf        bytes.Buffer
}

func (g *grpcChunkReader) Read(p []byte) (int, error) {
	totalRead := 0
	for totalRead < len(p) {
		//if there is data in the internal buffer -> read from it first
		if g.buf.Len() > 0 {
			n, err := g.buf.Read(p[totalRead:])
			totalRead += n
			if err != nil {
				return totalRead, unwrap(err)
			}
			//if the buffer is full -> done
			if totalRead == len(p) {
				return totalRead, nil
			}
		}

		//receive the next chunk of data
		chunk, err := g.streamRecv()
		if err != nil {
			if err == io.EOF {
				if totalRead > 0 {
					return totalRead, nil //return what we have before signaling EOF
				}
				return 0, io.EOF
			}
			return totalRead, fmt.Errorf("failed to receive file data: %w", unwrap(err))
		}

		//add chunk to the internal buffer
		g.buf.Write(chunk)
	}

	return totalRead, nil
}

// Actual closing is done by the SendChunks function whenever it has read the
// full content of the Reader. We do not actually forward the Close call over
// GRPC, but we should at some point.
func (g *grpcChunkReader) Close() error {
	return nil
}

func ReceiveChunks(chunkReceiverFn func() ([]byte, error)) io.ReadCloser {
	streamReader := &grpcChunkReader{
		streamRecv: chunkReceiverFn,
	}
	return streamReader
}

func toGrpcMAC(mac objects.MAC) *grpc_storage.MAC {
	return &grpc_storage.MAC{Value: mac[:]}
}

func (s *GrpcStorage) GetStates(ctx context.Context) ([]objects.MAC, error) {
	resp, err := s.GrpcClient.GetStates(ctx, &grpc_storage.GetStatesRequest{
		Cookie: s.cookie,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get states: %w", unwrap(err))
	}

	var states []objects.MAC
	for _, mac := range resp.Macs {
		if len(mac.Value) != len(objects.MAC{}) {
			return nil, fmt.Errorf("invalid MAC length: %d", len(mac.Value))
		}
		states = append(states, objects.MAC(mac.Value))
	}
	return states, nil
}

func (s *GrpcStorage) PutState(ctx context.Context, mac objects.MAC, rd io.Reader) (int64, error) {
	stream, err := s.GrpcClient.PutState(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to start PutState stream: %w", unwrap(err))
	}

	err = stream.Send(&grpc_storage.PutStateRequest{
		Mac:    toGrpcMAC(mac),
		Cookie: s.cookie,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to send MAC: %w", unwrap(err))
	}

	n, err := SendChunks(io.NopCloser(rd), func(chunk []byte) error {
		return stream.Send(&grpc_storage.PutStateRequest{
			Chunk:  chunk,
			Cookie: s.cookie,
		})
	})
	if err != nil {
		_, _ = stream.CloseAndRecv()
		return n, unwrap(err)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return n, unwrap(err)
	}
	return resp.BytesWritten, nil
}

func (s *GrpcStorage) GetState(ctx context.Context, mac objects.MAC) (io.ReadCloser, error) {
	stream, err := s.GrpcClient.GetState(ctx, &grpc_storage.GetStateRequest{
		Mac:    toGrpcMAC(mac),
		Cookie: s.cookie,
	})
	if err != nil {
		return nil, fmt.Errorf("get state: %w", unwrap(err))
	}

	return ReceiveChunks(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, unwrap(err)
		}
		return resp.Chunk, nil
	}), nil
}

func (s *GrpcStorage) DeleteState(ctx context.Context, mac objects.MAC) error {
	_, err := s.GrpcClient.DeleteState(ctx, &grpc_storage.DeleteStateRequest{
		Mac:    toGrpcMAC(mac),
		Cookie: s.cookie,
	})
	if err != nil {
		return fmt.Errorf("failed to delete state: %w", unwrap(err))
	}
	return nil
}

func (s *GrpcStorage) GetPackfiles(ctx context.Context) ([]objects.MAC, error) {
	resp, err := s.GrpcClient.GetPackfiles(ctx, &grpc_storage.GetPackfilesRequest{
		Cookie: s.cookie,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get packfiles: %w", unwrap(err))
	}

	var packfiles []objects.MAC
	for _, mac := range resp.Macs {
		if len(mac.Value) != len(objects.MAC{}) {
			return nil, fmt.Errorf("invalid MAC length: %d", len(mac.Value))
		}
		packfiles = append(packfiles, objects.MAC(mac.Value))
	}
	return packfiles, nil
}

func (s *GrpcStorage) PutPackfile(ctx context.Context, mac objects.MAC, rd io.Reader) (int64, error) {
	stream, err := s.GrpcClient.PutPackfile(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to start PutPackfile stream: %w", unwrap(err))
	}

	err = stream.Send(&grpc_storage.PutPackfileRequest{
		Mac:    toGrpcMAC(mac),
		Cookie: s.cookie,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to send MAC: %w", unwrap(err))
	}

	n, err := SendChunks(io.NopCloser(rd), func(chunk []byte) error {
		return stream.Send(&grpc_storage.PutPackfileRequest{
			Chunk:  chunk,
			Cookie: s.cookie,
		})
	})
	if err != nil {
		_, _ = stream.CloseAndRecv()
		return n, unwrap(err)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return n, unwrap(err)
	}
	return resp.BytesWritten, nil
}

func (s *GrpcStorage) GetPackfile(ctx context.Context, mac objects.MAC) (io.ReadCloser, error) {
	stream, err := s.GrpcClient.GetPackfile(ctx, &grpc_storage.GetPackfileRequest{
		Mac:    toGrpcMAC(mac),
		Cookie: s.cookie,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get packfile: %w", unwrap(err))
	}

	return ReceiveChunks(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, unwrap(err)
		}
		return resp.Chunk, nil
	}), nil
}

func (s *GrpcStorage) GetPackfileBlob(ctx context.Context, mac objects.MAC, offset uint64, length uint32) (io.ReadCloser, error) {
	stream, err := s.GrpcClient.GetPackfileBlob(ctx, &grpc_storage.GetPackfileBlobRequest{
		Mac:    toGrpcMAC(mac),
		Offset: offset,
		Length: length,
		Cookie: s.cookie,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get packfile blob: %w", unwrap(err))
	}

	return ReceiveChunks(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, unwrap(err)
		}
		return resp.Chunk, nil
	}), nil
}

func (s *GrpcStorage) DeletePackfile(ctx context.Context, mac objects.MAC) error {
	_, err := s.GrpcClient.DeletePackfile(ctx, &grpc_storage.DeletePackfileRequest{
		Mac:    toGrpcMAC(mac),
		Cookie: s.cookie,
	})
	if err != nil {
		return fmt.Errorf("failed to delete packfile: %w", unwrap(err))
	}
	return nil
}

func (s *GrpcStorage) GetLocks(ctx context.Context) ([]objects.MAC, error) {
	resp, err := s.GrpcClient.GetLocks(ctx, &grpc_storage.GetLocksRequest{
		Cookie: s.cookie,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get locks: %w", unwrap(err))
	}

	var locks []objects.MAC
	for _, mac := range resp.Macs {
		if len(mac.Value) != len(objects.MAC{}) {
			return nil, fmt.Errorf("invalid MAC length: %d", len(mac.Value))
		}
		locks = append(locks, objects.MAC(mac.Value))
	}
	return locks, nil
}

func (s *GrpcStorage) PutLock(ctx context.Context, lockID objects.MAC, rd io.Reader) (int64, error) {
	stream, err := s.GrpcClient.PutLock(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to start PutLock stream: %w", unwrap(err))
	}

	err = stream.Send(&grpc_storage.PutLockRequest{
		Mac:    toGrpcMAC(lockID),
		Cookie: s.cookie,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to send MAC: %w", unwrap(err))
	}

	n, err := SendChunks(io.NopCloser(rd), func(chunk []byte) error {
		return stream.Send(&grpc_storage.PutLockRequest{
			Chunk:  chunk,
			Cookie: s.cookie,
		})
	})
	if err != nil {
		_, _ = stream.CloseAndRecv()
		return n, unwrap(err)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return n, unwrap(err)
	}
	return resp.BytesWritten, nil
}

func (s *GrpcStorage) GetLock(ctx context.Context, lockID objects.MAC) (io.ReadCloser, error) {
	stream, err := s.GrpcClient.GetLock(ctx, &grpc_storage.GetLockRequest{
		Mac:    toGrpcMAC(lockID),
		Cookie: s.cookie,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get lock: %w", unwrap(err))
	}

	return ReceiveChunks(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, unwrap(err)
		}
		return resp.Chunk, nil
	}), nil
}

func (s *GrpcStorage) DeleteLock(ctx context.Context, lockID objects.MAC) error {
	_, err := s.GrpcClient.DeleteLock(ctx, &grpc_storage.DeleteLockRequest{
		Mac:    toGrpcMAC(lockID),
		Cookie: s.cookie,
	})
	if err != nil {
		return fmt.Errorf("failed to delete lock: %w", unwrap(err))
	}
	return nil
}

func (s *GrpcStorage) Close(ctx context.Context) error {
	_, err := s.GrpcClient.Close(ctx, &grpc_storage.CloseRequest{Cookie: s.cookie})
	return unwrap(err)
}
