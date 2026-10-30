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

// Package integration exercises the SMB connectors against a real Samba in a
// container. Run with -short to skip it.
package integration

import (
	"context"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// Samba 4.13 on Alpine: no reparse point support, so symlinks on the
	// share are resolved by the server and invisible to the client, which
	// is the server class PlakarKorp/security#50 was reported against.
	image    = "dperson/samba@sha256:66088b78a19810dd1457a8f39340e95e663c728083efa5fe7dc0d40b2478e869"
	username = "plakar"
	password = "plakar"
	shareDir = "/share"
	share    = "data"
)

var (
	serverOnce sync.Once
	serverCtr  testcontainers.Container
	serverAddr string
	serverErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if serverCtr != nil {
		_ = serverCtr.Terminate(context.Background(), testcontainers.StopTimeout(0))
	}
	os.Exit(code)
}

// server returns the host:port of the shared Samba server, starting it on
// first use.
func server(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	serverOnce.Do(func() {
		serverAddr, serverErr = startServer(context.Background())
	})
	require.NoError(t, serverErr)
	return serverAddr
}

func startServer(ctx context.Context) (string, error) {
	req := testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: []string{"445/tcp"},
		// One user, one writable share it administers, so that what the
		// tests plant as root in the container is writable through SMB.
		Cmd: []string{
			"-u", username + ";" + password,
			"-s", share + ";" + shareDir + ";yes;no;no;" + username + ";" + username,
		},
		WaitingFor: wait.ForListeningPort("445/tcp"),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	serverCtr = ctr
	if err != nil {
		return "", err
	}

	port, err := ctr.MappedPort(ctx, "445/tcp")
	if err != nil {
		return "", err
	}
	return "127.0.0.1:" + port.Port(), nil
}

// sh runs a shell command inside the container, as root, and returns its
// output.
func sh(t *testing.T, cmd string) string {
	t.Helper()
	code, rd, err := serverCtr.Exec(context.Background(), []string{"sh", "-c", cmd}, tcexec.Multiplexed())
	require.NoError(t, err)
	out, err := io.ReadAll(rd)
	require.NoError(t, err)
	require.Zero(t, code, "%s: %s", cmd, out)
	return string(out)
}

// exists reports whether p, relative to the share, exists on the server.
func exists(t *testing.T, p string) bool {
	t.Helper()
	code, _, err := serverCtr.Exec(context.Background(), []string{"test", "-e", shareDir + p})
	require.NoError(t, err)
	return code == 0
}
