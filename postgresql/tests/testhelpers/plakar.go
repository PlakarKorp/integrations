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
// from this source file.  Reliable regardless of where `go test` is invoked.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	// file is tests/testhelpers/plakar.go — three Dir calls reach the repo root.
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// StartPlakarContainer starts a container from the plakar test image with the
// postgresql plugin built and installed from the mounted source tree.
// networkName is an optional Docker network to attach the container to; pass
// an empty string when no extra network is needed.
// The container is automatically terminated when the test ends.
func StartPlakarContainer(ctx context.Context, t *testing.T, networkName string) testcontainers.Container {
	t.Helper()
	root := repoRoot()
	sha := plakarSHA

	var networks []string
	if networkName != "" {
		networks = []string{networkName}
	}

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:       root,
			Dockerfile:    "tests/plakar.Dockerfile",
			BuildArgs:     map[string]*string{"PLAKAR_SHA": &sha},
			KeepImage:     true,
			PrintBuildLog: false,
		},
		Cmd: []string{"sleep", "infinity"},
		Mounts: testcontainers.ContainerMounts{
			testcontainers.BindMount(root, "/src"),
		},
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

	installScript := `set -e
mkdir -p /tmp/pgpkg
cd /src
go build -o /tmp/pgpkg/postgresqlImporter    ./plugin/importer
go build -o /tmp/pgpkg/postgresqlExporter    ./plugin/exporter
go build -o /tmp/pgpkg/postgresqlBinImporter ./plugin/binimporter
cp /src/manifest.yaml /tmp/pgpkg/
cd /tmp/pgpkg
GOOS=$(go env GOOS)
GOARCH=$(go env GOARCH)
PTAR="postgresql_v0.0.1_${GOOS}_${GOARCH}.ptar"
rm -f "${PTAR}"
plakar pkg create ./manifest.yaml v0.0.1
plakar pkg add "./${PTAR}"`

	ExecOK(ctx, t, container, "sh", "-c", installScript)
	return container
}
