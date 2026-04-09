package testhelpers

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// plakarSHA is the plakar commit installed in the test image.
// Override via PLAKAR_SHA build arg when rebuilding the image manually.
const plakarSHA = "main"

// repoRoot returns the absolute path of the repository root by walking up
// from this source file. Reliable regardless of where `go test` is invoked.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	// file is tests/testhelpers/plakar.go — three Dir calls reach the repo root.
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// StartPlakarContainer starts a container from the plakar test image.
// The image already has plakar and the mysql plugin installed (built during
// the Docker image build from the repository source).
// net is an optional Docker network to attach the container to; pass nil
// when no extra network is needed.
// The container is automatically terminated when the test ends.
func StartPlakarContainer(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork) testcontainers.Container {
	t.Helper()
	root := repoRoot()
	sha := plakarSHA

	var networks []string
	if net != nil {
		networks = []string{net.Name}
	}

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:       root,
			Dockerfile:    "tests/plakar.Dockerfile",
			BuildArgs:     map[string]*string{"PLAKAR_SHA": &sha},
			KeepImage:     true,
			PrintBuildLog: true,
		},
		Cmd:      []string{"sleep", "infinity"},
		Networks: networks,
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start plakar container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	return container
}
