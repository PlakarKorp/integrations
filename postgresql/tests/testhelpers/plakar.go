package testhelpers

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

// plakarSHA is the plakar commit installed in the test image.
// Override via PLAKAR_SHA build arg when rebuilding the image manually.
// Pinned to a plakar branch with the kloset warm stage fix, which the FUSE
// importer needs to back up with the VFS cache on.
const plakarSHA = "6599868b"

// repoRoot returns the absolute path of the repository root by walking up
// from this source file.  Reliable regardless of where `go test` is invoked.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	// file is tests/testhelpers/plakar.go — three Dir calls reach the repo root.
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// StartPlakarContainer starts a container from the plakar test image.
// The image already has plakar and the postgresql plugin installed (built
// during the Docker image build from the repository source).
// net is an optional Docker network to attach the container to; pass nil
// when no extra network is needed.
// The container is automatically terminated when the test ends.
func StartPlakarContainer(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork) testcontainers.Container {
	t.Helper()
	return startPlakarContainer(ctx, t, net, nil)
}

// StartPlakarFUSEContainer is StartPlakarContainer with the privileges a FUSE
// mount needs, for backups with format=directory.
func StartPlakarFUSEContainer(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork) testcontainers.Container {
	t.Helper()
	return startPlakarContainer(ctx, t, net, func(hc *container.HostConfig) {
		hc.Devices = append(hc.Devices, container.DeviceMapping{
			PathOnHost: "/dev/fuse", PathInContainer: "/dev/fuse", CgroupPermissions: "rwm",
		})
		hc.CapAdd = append(hc.CapAdd, "SYS_ADMIN")
		// Docker's default AppArmor profile denies mount(2).
		hc.SecurityOpt = append(hc.SecurityOpt, "apparmor:unconfined")
	})
}

func startPlakarContainer(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork, hostConfig func(*container.HostConfig)) testcontainers.Container {
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
			PrintBuildLog: false,
		},
		Cmd:      []string{"sleep", "infinity"},
		Networks: networks,
		// The tests use a -plaintext store, which plakar refuses without it.
		Env:                map[string]string{"PLAKAR_INSECURE_PLAINTEXT": "1"},
		HostConfigModifier: hostConfig,
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
