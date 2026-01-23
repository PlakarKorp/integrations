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
	"errors"
	"fmt"
	"io"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpc_storage "github.com/PlakarKorp/integration-grpc/storage/pkg"
)

type GrpcStorage struct {
	GrpcClient grpc_storage.StoreClient
	cookie     string
	typ        string
	origin     string
	root       string
	mode       storage.Mode
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
	s := &GrpcStorage{
		GrpcClient: grpc_storage.NewStoreClient(client),
	}

	res, err := s.GrpcClient.Init(ctx, &grpc_storage.InitRequest{
		Proto:  proto,
		Config: config,
	})
	if err != nil {
		return nil, unwrap(err)
	}

	s.cookie = res.Cookie

	if resp, err := s.GrpcClient.GetLocation(ctx, &grpc_storage.GetLocationRequest{
		Cookie: s.cookie,
	}); err != nil {
		s.GrpcClient.Close(ctx, &grpc_storage.CloseRequest{Cookie: s.cookie})
		return nil, unwrap(err)
	} else {
		s.origin = resp.Location
	}

	if resp, err := s.GrpcClient.GetMode(ctx, &grpc_storage.GetModeRequest{
		Cookie: s.cookie,
	}); err != nil {
		s.GrpcClient.Close(ctx, &grpc_storage.CloseRequest{Cookie: s.cookie})
		return nil, unwrap(err)
	} else {
		s.mode = storage.Mode(resp.Mode)
	}

	return s, nil
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

func (s *GrpcStorage) Ping(ctx context.Context) error {
	return nil
}

func (s *GrpcStorage) Origin() string {
	return s.origin
}

func (s *GrpcStorage) Mode() storage.Mode {
	return s.mode
}

func (s *GrpcStorage) Root() string {
	// Old protocol didn't have this, no way to get it?
	return ""
}

func (s *GrpcStorage) Type() string {
	// Old protocol didn't have this, no way to get it?
	return "grpc"
}

func (s *GrpcStorage) Flags() location.Flags {
	// Old protocol didn't have this, no way to get it?
	return 0
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

// Closing of the actual reader is left to the caller, close is not to be
// forwarded over grpc.
// We still drain this in order to avoid a misuse of the API (where someone
// would request less than what the server is sending), which leads to leaks.
func (g *grpcChunkReader) Close() error {
	for {
		_, err := g.streamRecv()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
	}
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

func (s *GrpcStorage) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return s.getPackfiles(ctx)
	case storage.StorageResourceStatefile:
		return s.getStates(ctx)
	case storage.StorageResourceLockfile:
		return s.getLocks(ctx)
	}

	return nil, errors.ErrUnsupported
}

func (s *GrpcStorage) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	switch res {
	case storage.StorageResourcePackfile:
		return s.putPackfile(ctx, mac, rd)
	case storage.StorageResourceStatefile:
		return s.putState(ctx, mac, rd)
	case storage.StorageResourceLockfile:
		return s.putLock(ctx, mac, rd)
	}

	return -1, errors.ErrUnsupported
}

func (s *GrpcStorage) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	switch res {
	case storage.StorageResourcePackfile:
		if rg != nil {
			return s.getPackfileBlob(ctx, mac, rg.Offset, rg.Length)
		} else {
			return s.getPackfile(ctx, mac)
		}
	case storage.StorageResourceStatefile:
		return s.getState(ctx, mac)
	case storage.StorageResourceLockfile:
		return s.getLock(ctx, mac)
	}

	return nil, errors.ErrUnsupported
}

func (s *GrpcStorage) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	switch res {
	case storage.StorageResourcePackfile:
		return s.deletePackfile(ctx, mac)
	case storage.StorageResourceStatefile:
		return s.deleteState(ctx, mac)
	case storage.StorageResourceLockfile:
		return s.deleteLock(ctx, mac)
	}

	return errors.ErrUnsupported
}

func (s *GrpcStorage) getStates(ctx context.Context) ([]objects.MAC, error) {
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

func (s *GrpcStorage) putState(ctx context.Context, mac objects.MAC, rd io.Reader) (int64, error) {
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
		if err == io.EOF {
			_, err = stream.CloseAndRecv()
		}
		return n, unwrap(err)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return n, unwrap(err)
	}
	return resp.BytesWritten, nil
}

func (s *GrpcStorage) getState(ctx context.Context, mac objects.MAC) (io.ReadCloser, error) {
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

func (s *GrpcStorage) deleteState(ctx context.Context, mac objects.MAC) error {
	_, err := s.GrpcClient.DeleteState(ctx, &grpc_storage.DeleteStateRequest{
		Mac:    toGrpcMAC(mac),
		Cookie: s.cookie,
	})
	if err != nil {
		return fmt.Errorf("failed to delete state: %w", unwrap(err))
	}
	return nil
}

func (s *GrpcStorage) getPackfiles(ctx context.Context) ([]objects.MAC, error) {
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

func (s *GrpcStorage) putPackfile(ctx context.Context, mac objects.MAC, rd io.Reader) (int64, error) {
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
		if err == io.EOF {
			_, err = stream.CloseAndRecv()
		}
		return n, unwrap(err)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return n, unwrap(err)
	}
	return resp.BytesWritten, nil
}

func (s *GrpcStorage) getPackfile(ctx context.Context, mac objects.MAC) (io.ReadCloser, error) {
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

func (s *GrpcStorage) getPackfileBlob(ctx context.Context, mac objects.MAC, offset uint64, length uint32) (io.ReadCloser, error) {
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

func (s *GrpcStorage) deletePackfile(ctx context.Context, mac objects.MAC) error {
	_, err := s.GrpcClient.DeletePackfile(ctx, &grpc_storage.DeletePackfileRequest{
		Mac:    toGrpcMAC(mac),
		Cookie: s.cookie,
	})
	if err != nil {
		return fmt.Errorf("failed to delete packfile: %w", unwrap(err))
	}
	return nil
}

func (s *GrpcStorage) getLocks(ctx context.Context) ([]objects.MAC, error) {
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

func (s *GrpcStorage) putLock(ctx context.Context, lockID objects.MAC, rd io.Reader) (int64, error) {
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
		if err == io.EOF {
			_, err = stream.CloseAndRecv()
		}
		return n, unwrap(err)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return n, unwrap(err)
	}
	return resp.BytesWritten, nil
}

func (s *GrpcStorage) getLock(ctx context.Context, lockID objects.MAC) (io.ReadCloser, error) {
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

func (s *GrpcStorage) deleteLock(ctx context.Context, lockID objects.MAC) error {
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
