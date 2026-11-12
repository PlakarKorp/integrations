/*
 * Copyright (c) 2025 Gilles Chehade <gilles@poolp.org>
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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	sambaOnce     sync.Once
	sambaLocation string
	errSamba      error
)

// startSamba starts one Samba server for the whole package and returns the
// smb:// location of its share. Tests are skipped when Docker is unavailable.
func startSamba(t *testing.T) string {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)

	sambaOnce.Do(func() {
		ctx := context.Background()
		c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        "dockurr/samba@sha256:2a0452a12b7a244a22f18d0f233101bb8d4952b5500ae263905e3b9dad3bf2ae",
				ExposedPorts: []string{"445/tcp"},
				Env: map[string]string{
					"NAME": "data",
					"USER": "alice",
					"PASS": "secret",
				},
				WaitingFor: wait.ForListeningPort("445/tcp").WithStartupTimeout(2 * time.Minute),
			},
			Started: true,
		})
		if err != nil {
			errSamba = err
			return
		}
		host, err := c.Host(ctx)
		if err != nil {
			errSamba = err
			return
		}
		port, err := c.MappedPort(ctx, "445/tcp")
		if err != nil {
			errSamba = err
			return
		}
		sambaLocation = "smb://alice:secret@" + host + ":" + port.Port() + "/data"
	})
	require.NoError(t, errSamba)
	return sambaLocation
}

// newTestStore returns a store rooted at a fresh directory of the share.
func newTestStore(t *testing.T) storage.Store {
	t.Helper()
	location := startSamba(t)

	var buf [4]byte
	_, _ = rand.Read(buf[:])
	root := "/" + t.Name() + "-" + hex.EncodeToString(buf[:])

	ctx := context.Background()
	var st storage.Store
	var err error
	// Samba may accept TCP connections slightly before it is ready to
	// authenticate; retry the first connection for a little while.
	for range 20 {
		st, err = NewStore(ctx, "smb", map[string]string{"location": location, "root": root})
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close(ctx) })
	return st
}

func randomMAC(t *testing.T) objects.MAC {
	var mac objects.MAC
	_, err := rand.Read(mac[:])
	require.NoError(t, err)
	return mac
}

func TestStoreCreateAndOpen(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	config := []byte("hello config")
	require.NoError(t, st.Create(ctx, config))

	got, err := st.Open(ctx)
	require.NoError(t, err)
	require.Equal(t, config, got)

	require.NoError(t, st.Ping(ctx))

	// A second Create on the now non-empty root must fail.
	require.Error(t, st.Create(ctx, config))
}

func TestStorePackfileRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	require.NoError(t, st.Create(ctx, []byte("cfg")))

	mac := randomMAC(t)
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz")

	n, err := st.Put(ctx, storage.StorageResourcePackfile, mac, bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), n)

	// Putting the same content-addressed object again is a no-op success.
	n, err = st.Put(ctx, storage.StorageResourcePackfile, mac, bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), n)

	macs, err := st.List(ctx, storage.StorageResourcePackfile)
	require.NoError(t, err)
	require.Equal(t, []objects.MAC{mac}, macs)

	rd, err := st.Get(ctx, storage.StorageResourcePackfile, mac, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(rd)
	require.NoError(t, rd.Close())
	require.NoError(t, err)
	require.Equal(t, data, got)

	rd, err = st.Get(ctx, storage.StorageResourcePackfile, mac, &storage.Range{Offset: 10, Length: 6})
	require.NoError(t, err)
	got, err = io.ReadAll(rd)
	require.NoError(t, rd.Close())
	require.NoError(t, err)
	require.Equal(t, []byte("abcdef"), got)

	require.NoError(t, st.Delete(ctx, storage.StorageResourcePackfile, mac))
	macs, err = st.List(ctx, storage.StorageResourcePackfile)
	require.NoError(t, err)
	require.Empty(t, macs)

	_, err = st.Get(ctx, storage.StorageResourcePackfile, mac, nil)
	require.ErrorIs(t, err, repository.ErrPackfileNotFound)
}

func TestStoreStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	require.NoError(t, st.Create(ctx, []byte("cfg")))

	mac := randomMAC(t)
	data := []byte("state payload")

	_, err := st.Put(ctx, storage.StorageResourceState, mac, bytes.NewReader(data))
	require.NoError(t, err)

	macs, err := st.List(ctx, storage.StorageResourceState)
	require.NoError(t, err)
	require.Equal(t, []objects.MAC{mac}, macs)

	rd, err := st.Get(ctx, storage.StorageResourceState, mac, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(rd)
	require.NoError(t, rd.Close())
	require.NoError(t, err)
	require.Equal(t, data, got)

	_, err = st.Get(ctx, storage.StorageResourceState, mac, &storage.Range{Offset: 0, Length: 1})
	require.ErrorIs(t, err, errors.ErrUnsupported)

	require.NoError(t, st.Delete(ctx, storage.StorageResourceState, mac))
	macs, err = st.List(ctx, storage.StorageResourceState)
	require.NoError(t, err)
	require.Empty(t, macs)
}

func TestStoreLockRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	require.NoError(t, st.Create(ctx, []byte("cfg")))

	mac := randomMAC(t)
	data := []byte("lock payload")

	_, err := st.Put(ctx, storage.StorageResourceLock, mac, bytes.NewReader(data))
	require.NoError(t, err)

	macs, err := st.List(ctx, storage.StorageResourceLock)
	require.NoError(t, err)
	require.Equal(t, []objects.MAC{mac}, macs)

	rd, err := st.Get(ctx, storage.StorageResourceLock, mac, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(rd)
	require.NoError(t, rd.Close())
	require.NoError(t, err)
	require.Equal(t, data, got)

	require.NoError(t, st.Delete(ctx, storage.StorageResourceLock, mac))
	macs, err = st.List(ctx, storage.StorageResourceLock)
	require.NoError(t, err)
	require.Empty(t, macs)
}
