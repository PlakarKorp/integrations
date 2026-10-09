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
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/go-connections/nat"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// nfsServerPorts are the fixed ports erichough/nfs-server binds its ancillary
// RPC services to (rpcbind, nfsd, mountd, statd). NFSv3 clients learn these
// from the portmapper as the container sees them, so the host-side publish
// must use the identical port numbers or the handshake breaks: there's no
// way to tell the client "mountd is really on a different port than the one
// the server just told you".
var nfsServerPorts = []string{"111", "2049", "32765", "32767"}

// startNFSServer starts an erichough/nfs-server container exporting an empty
// directory, with its RPC ports published on the same numbers on the host
// (see nfsServerPorts), and returns a connector config pointing at it.
func startNFSServer(t *testing.T) map[string]string {
	t.Helper()
	tc.SkipIfProviderIsNotHealthy(t)

	ctx := context.Background()

	exposed := make([]string, 0, len(nfsServerPorts))
	bindings := nat.PortMap{}
	for _, p := range nfsServerPorts {
		port := nat.Port(p + "/tcp")
		exposed = append(exposed, string(port))
		bindings[port] = []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: p}}
	}

	req := tc.ContainerRequest{
		Image:        "erichough/nfs-server:latest",
		ExposedPorts: exposed,
		Env: map[string]string{
			"NFS_VERSION":  "3",
			"NFS_EXPORT_0": "/export *(rw,insecure,no_subtree_check,no_root_squash,sync)",
		},
		// The export directory needs to exist before the entrypoint runs
		// exportfs; copying a placeholder file in creates it.
		Files: []tc.ContainerFile{{
			Reader:            strings.NewReader(""),
			ContainerFilePath: "/export/.keep",
			FileMode:          0o644,
		}},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.CapAdd = strslice.StrSlice{"SYS_ADMIN"}
			hc.PortBindings = bindings
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("111/tcp"),
			wait.ForListeningPort("2049/tcp"),
		).WithDeadline(60 * time.Second),
	}

	c, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		// NFSv3 needs its ancillary RPC ports (mountd, statd) published on
		// the exact same number the portmapper advertises them under, so
		// unlike most testcontainers setups these can't be randomized. On
		// Docker Desktop for Mac, 111/2049 are sometimes already bound by
		// Docker's own NFS-based volume sharing; skip rather than fail in
		// that case.
		if strings.Contains(err.Error(), "address already in use") {
			t.Skipf("nfs-server container port already in use on this host (Docker Desktop's own NFS volume sharing is a common culprit): %v", err)
		}
		t.Fatalf("start nfs-server container: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	return map[string]string{
		"location": "nfs://127.0.0.1/export",
	}
}

// newTestStore opens a Store against a freshly started NFS server, retrying
// briefly: the RPC ports can be listening slightly before rpcbind has
// registered mountd/nfsd with it.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	cfg := startNFSServer(t)

	var (
		st  storage.Store
		err error
	)
	for range 10 {
		st, err = NewStore(context.Background(), "nfs", cfg)
		if err == nil {
			if err = st.Ping(context.Background()); err == nil {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("connect to nfs-server: %v", err)
	}

	return st.(*Store)
}

func randomMAC(t *testing.T) objects.MAC {
	t.Helper()
	var mac objects.MAC
	if _, err := rand.Read(mac[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return mac
}

func TestStoreCreateAndOpen(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close(context.Background()) }()

	config := []byte("hello configuration")
	if err := s.Create(context.Background(), config); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, config) {
		t.Errorf("Open() = %q, want %q", got, config)
	}
}

func TestStorePackfileRoundTrip(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close(context.Background()) }()

	if err := s.Create(context.Background(), []byte("config")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	mac := randomMAC(t)
	content := bytes.Repeat([]byte("packfile-bytes-"), 1024) // exercise more than one NFS READ round-trip

	n, err := s.Put(context.Background(), storage.StorageResourcePackfile, mac, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != int64(len(content)) {
		t.Errorf("Put returned %d bytes, want %d", n, len(content))
	}

	macs, err := s.List(context.Background(), storage.StorageResourcePackfile)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(macs) != 1 || macs[0] != mac {
		t.Errorf("List = %v, want [%v]", macs, mac)
	}

	rd, err := s.Get(context.Background(), storage.StorageResourcePackfile, mac, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rd)
	_ = rd.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Get() = %d bytes, want %d bytes matching what was Put", len(got), len(content))
	}

	// Partial read, exercising OpenReaderRange.
	rg := &storage.Range{Offset: 10, Length: 20}
	rdRange, err := s.Get(context.Background(), storage.StorageResourcePackfile, mac, rg)
	if err != nil {
		t.Fatalf("Get (range): %v", err)
	}
	gotRange, err := io.ReadAll(rdRange)
	_ = rdRange.Close()
	if err != nil {
		t.Fatalf("read range: %v", err)
	}
	if want := content[10:30]; !bytes.Equal(gotRange, want) {
		t.Errorf("ranged Get() = %q, want %q", gotRange, want)
	}

	if err := s.Delete(context.Background(), storage.StorageResourcePackfile, mac); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(context.Background(), storage.StorageResourcePackfile, mac, nil); err == nil {
		t.Errorf("Get() after Delete: expected an error, got nil")
	}
}

func TestStoreStateRoundTrip(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close(context.Background()) }()

	if err := s.Create(context.Background(), []byte("config")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	mac := randomMAC(t)
	content := []byte("state-blob")

	if _, err := s.Put(context.Background(), storage.StorageResourceState, mac, bytes.NewReader(content)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rd, err := s.Get(context.Background(), storage.StorageResourceState, mac, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rd)
	_ = rd.Close()
	if err != nil || !bytes.Equal(got, content) {
		t.Errorf("Get() = %q, %v, want %q, nil", got, err, content)
	}

	if err := s.Delete(context.Background(), storage.StorageResourceState, mac); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestStoreLockRoundTrip(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close(context.Background()) }()

	if err := s.Create(context.Background(), []byte("config")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	mac := randomMAC(t)
	content := []byte("lock-holder-id")

	if _, err := s.Put(context.Background(), storage.StorageResourceLock, mac, bytes.NewReader(content)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	macs, err := s.List(context.Background(), storage.StorageResourceLock)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(macs) != 1 || macs[0] != mac {
		t.Errorf("List = %v, want [%v]", macs, mac)
	}

	if err := s.Delete(context.Background(), storage.StorageResourceLock, mac); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	macs, err = s.List(context.Background(), storage.StorageResourceLock)
	if err != nil {
		t.Fatalf("List after Delete: %v", err)
	}
	if len(macs) != 0 {
		t.Errorf("List after Delete = %v, want empty", macs)
	}
}

func TestStoreGetMissingPackfileReturnsErrPackfileNotFound(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close(context.Background()) }()

	if err := s.Create(context.Background(), []byte("config")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := s.Get(context.Background(), storage.StorageResourcePackfile, randomMAC(t), nil)
	if !errors.Is(err, repository.ErrPackfileNotFound) {
		t.Errorf("Get() on a missing packfile = %v, want repository.ErrPackfileNotFound", err)
	}
}
