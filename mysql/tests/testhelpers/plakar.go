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

// PreBuildPlakarImage builds the plakar test image for the given variant and
// keeps it in the local Docker image cache. Calling this before running tests
// avoids rebuilding the image for each test case.
func PreBuildPlakarImage(ctx context.Context, v DBVariant) error {
	sha := plakarSHA
	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:       repoRoot(),
			Dockerfile:    v.Dockerfile,
			BuildArgs:     map[string]*string{"PLAKAR_SHA": &sha},
			KeepImage:     true,
			Repo:          v.ImageTag,
			Tag:           "latest",
			PrintBuildLog: true,
		},
		Cmd: []string{"true"},
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return err
	}
	return c.Terminate(ctx)
}

// StartPlakarContainer starts a container from the plakar test image for the
// given variant. The image already has plakar and the mysql plugin installed.
// net is an optional Docker network to attach the container to; pass nil when
// no extra network is needed. The container is automatically terminated when
// the test ends.
func StartPlakarContainer(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork, v DBVariant) testcontainers.Container {
	t.Helper()
	sha := plakarSHA

	var networks []string
	if net != nil {
		networks = []string{net.Name}
	}

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:       repoRoot(),
			Dockerfile:    v.Dockerfile,
			BuildArgs:     map[string]*string{"PLAKAR_SHA": &sha},
			KeepImage:     true,
			Repo:          v.ImageTag,
			Tag:           "latest",
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
		t.Fatalf("start plakar container (%s): %v", v.Name, err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	return container
}
