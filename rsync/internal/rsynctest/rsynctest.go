// Package rsynctest starts a real rsync daemon for tests, built from
// testdata/Dockerfile.
package rsynctest

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartRsyncd starts the daemon and returns its address. It skips the test
// when Docker is not available.
func StartRsyncd(t *testing.T) string {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)

	_, file, _, _ := runtime.Caller(0)
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:   filepath.Join(filepath.Dir(file), "testdata"),
				KeepImage: true,
			},
			ExposedPorts: []string{"873/tcp"},
			WaitingFor:   wait.ForListeningPort("873/tcp"),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, container)
	require.NoError(t, err)

	addr, err := container.PortEndpoint(ctx, "873/tcp", "")
	require.NoError(t, err)
	return addr
}
