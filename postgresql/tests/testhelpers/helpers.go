package testhelpers

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tcexec "github.com/testcontainers/testcontainers-go/exec"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PlakarSHA is the plakar commit installed in the test image.
// Override via PLAKAR_SHA build arg when rebuilding the image manually.
const PlakarSHA = "main"

// RepoRoot returns the absolute path of the repository root by walking up
// from this source file.  Reliable regardless of where `go test` is invoked.
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	// file is tests/testhelpers/helpers.go — three Dir calls reach the repo root.
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// ExecOK runs cmd inside container, streams the combined output to t.Log, and
// fails the test if the exit code is non-zero.
func ExecOK(ctx context.Context, t *testing.T, container testcontainers.Container, cmd ...string) {
	t.Helper()
	code, out, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("exec %v: %v", cmd, err)
	}
	b, _ := io.ReadAll(out)
	if s := strings.TrimSpace(string(b)); s != "" {
		t.Log(s)
	}
	if code != 0 {
		t.Fatalf("exec %v: exited %d", cmd, code)
	}
}

// ExecCapture runs cmd inside container and returns its combined output as a
// string.  The test fails if the exit code is non-zero.
func ExecCapture(ctx context.Context, t *testing.T, container testcontainers.Container, cmd ...string) string {
	t.Helper()
	code, out, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("exec %v: %v", cmd, err)
	}
	b, _ := io.ReadAll(out)
	s := strings.TrimSpace(string(b))
	if code != 0 {
		t.Fatalf("exec %v: exited %d\n%s", cmd, code, s)
	}
	return s
}

// StartPlakarContainer starts a container from the plakar test image with the
// postgresql plugin built and installed from the mounted source tree.
// networks is an optional list of Docker network names to attach the container
// to (in addition to the default bridge).
// The container is automatically terminated when the test ends.
func StartPlakarContainer(ctx context.Context, t *testing.T, networks []string) testcontainers.Container {
	t.Helper()
	root := RepoRoot()
	sha := PlakarSHA

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

// StartPostgresContainer starts a postgres:17 container attached to networkName
// with a default database named "testdb" (password "secret").
//
// The server is configured with wal_level=replica and a pg_hba.conf rule that
// allows replication connections from any host, so both logical and physical
// (pg_basebackup) backups work against the same container.
//
// The container is automatically terminated when the test ends.
func StartPostgresContainer(ctx context.Context, t *testing.T, networkName string) testcontainers.Container {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image: "postgres:17",
		// Pass wal_level=replica as a server flag so it takes effect before
		// the first checkpoint — ALTER SYSTEM would require a restart.
		Cmd: []string{"postgres", "-c", "wal_level=replica"},
		Env: map[string]string{
			"POSTGRES_PASSWORD": "secret",
			"POSTGRES_DB":       "testdb",
		},
		Networks:       []string{networkName},
		NetworkAliases: map[string][]string{networkName: {"postgres"}},
		WaitingFor:     wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	// Allow replication connections from any address inside the Docker network.
	// pg_hba.conf changes take effect immediately after a reload (no restart needed).
	// Use pg_reload_conf() instead of pg_ctl so this works when exec'd as root.
	ExecOK(ctx, t, container, "bash", "-c",
		`echo "host replication all all trust" >> "$PGDATA/pg_hba.conf" && psql -U postgres -c "SELECT pg_reload_conf()"`)


	return container
}
