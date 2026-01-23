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
)

type Store struct {
	client StoreClient
	root   string
	origin string
	typ    string
	mode   storage.Mode
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

func NewStorage(ctx context.Context, client grpc.ClientConnInterface, proto string, config map[string]string) (storage.Store, error) {
	s := &Store{
		client: NewStoreClient(client),
	}

	res, err := s.client.Init(ctx, &InitRequest{
		Proto:  proto,
		Config: config,
	})
	if err != nil {
		return nil, unwrap(err)
	}

	s.origin = res.Origin
	s.root = res.Root
	s.typ = res.Type
	s.flags = location.Flags(res.Flags)

	return s, nil
}

func (s *Store) Create(ctx context.Context, config []byte) error {
	_, err := s.client.Create(ctx, &CreateRequest{
		Config: config,
	})
	if err != nil {
		return unwrap(err)
	}
	return nil
}

func (s *Store) Open(ctx context.Context) ([]byte, error) {
	resp, err := s.client.Open(ctx, &OpenRequest{})
	if err != nil {
		return nil, unwrap(err)
	}
	return resp.Config, nil
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.Ping(ctx, &PingRequest{})

	return unwrap(err)
}

func (s *Store) Origin() string        { return s.origin }
func (s *Store) Type() string          { return s.typ }
func (s *Store) Root() string          { return s.root }
func (s *Store) Flags() location.Flags { return s.flags }

func (s *Store) Mode(ctx context.Context) (storage.Mode, error) {
	if resp, err := s.client.Mode(ctx, &ModeRequest{}); err != nil {
		return 0, unwrap(err)
	} else {
		return storage.Mode(resp.Mode), nil
	}
}

func (s *Store) Size(ctx context.Context) (int64, error) {
	resp, err := s.client.Size(ctx, &SizeRequest{})
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

func (s *Store) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	resp, err := s.client.List(ctx, &ListRequest{
		Type: StorageResource(res),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", res, unwrap(err))
	}

	var macs []objects.MAC
	for _, mac := range resp.Macs {
		if len(mac) != len(objects.MAC{}) {
			return nil, fmt.Errorf("invalid MAC length: %d", len(mac))
		}
		macs = append(macs, objects.MAC(mac))
	}
	return macs, nil
}

func (s *Store) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	stream, err := s.client.Put(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to start Put stream: %w", unwrap(err))
	}

	err = stream.Send(&PutRequest{
		Mac:  mac[:],
		Type: StorageResource(res),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to send mac: %w", unwrap(err))
	}

	n, err := SendChunks(io.NopCloser(rd), func(chunk []byte) error {
		return stream.Send(&PutRequest{
			Chunk: chunk,
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

func (s *Store) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	var grg *GetRequest_Range
	if rg != nil {
		grg = &GetRequest_Range{
			Offset: rg.Offset,
			Length: rg.Length,
		}
	}

	stream, err := s.client.Get(ctx, &GetRequest{
		Mac:   mac[:],
		Type:  StorageResource(res),
		Range: grg,
	})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", res, unwrap(err))
	}

	return ReceiveChunks(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, unwrap(err)
		}
		return resp.Chunk, nil
	}), nil
}

func (s *Store) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	_, err := s.client.Delete(ctx, &DeleteRequest{
		Mac:  mac[:],
		Type: StorageResource(res),
	})
	if err != nil {
		return fmt.Errorf("failed to delete %s: %w", res, unwrap(err))
	}
	return nil
}

func (s *Store) Close(ctx context.Context) error {
	_, err := s.client.Close(ctx, &CloseRequest{})
	return unwrap(err)
}
