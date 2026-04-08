//go:build integration

package tests

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tcexec "github.com/testcontainers/testcontainers-go/exec"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// plakarSHA is the plakar commit that is installed in the test image.
// Override via PLAKAR_SHA build arg when rebuilding the image manually.
const plakarSHA = "main"

// repoRoot returns the absolute path of the repository root by walking up from
// the test source file.  This is reliable regardless of where `go test` is
// invoked from.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(file))
}

// execOK runs cmd inside container, streams the combined output to t.Log, and
// fails the test if the exit code is non-zero.
func execOK(ctx context.Context, t *testing.T, container testcontainers.Container, cmd ...string) {
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

// TestInstallPlugin verifies that the postgresql integration can be built from
// source and installed into a running plakar instance.
func TestInstallPlugin(t *testing.T) {
	ctx := context.Background()
	container := startPlakarContainer(ctx, t, nil)

	t.Log("=== plakar pkg list ===")
	execOK(ctx, t, container, "plakar", "pkg", "list")
}

// execCapture runs cmd inside container and returns its combined output as a
// string.  The test fails if the exit code is non-zero.
func execCapture(ctx context.Context, t *testing.T, container testcontainers.Container, cmd ...string) string {
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

// startPlakarContainer starts a container from the plakar test image with the
// postgresql plugin built and installed from the mounted source tree.
// networks is an optional list of Docker network names to attach the container
// to (in addition to the default bridge).
// The container is automatically terminated when the test ends.
func startPlakarContainer(ctx context.Context, t *testing.T, networks []string) testcontainers.Container {
	t.Helper()
	root := repoRoot()
	sha := plakarSHA

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

	execOK(ctx, t, container, "sh", "-c", installScript)
	return container
}

// TestLogicalBackup verifies the full backup→restore cycle for a logical
// (postgres://) backup:
//  1. Spin up a PostgreSQL container pre-seeded with test data.
//  2. Spin up a plakar container with the plugin installed.
//  3. Create a plakar store, run a backup, inspect the snapshot.
func TestLogicalBackup(t *testing.T) {
	ctx := context.Background()

	// Create an isolated Docker network so the plakar container can reach the
	// postgres container by hostname.
	net, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	t.Cleanup(func() { _ = net.Remove(ctx) })

	// Step 1 — start a PostgreSQL container on the network.
	pgReq := testcontainers.ContainerRequest{
		Image: "postgres:17",
		Env: map[string]string{
			"POSTGRES_PASSWORD": "secret",
			"POSTGRES_DB":       "testdb",
		},
		Networks:       []string{net.Name},
		NetworkAliases: map[string][]string{net.Name: {"postgres"}},
		WaitingFor:     wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
	}
	pgContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: pgReq,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pgContainer.Terminate(ctx) })

	// Seed the database with a simple table.
	seedSQL := `CREATE TABLE users (id serial PRIMARY KEY, name text NOT NULL);
INSERT INTO users (name) VALUES ('alice'), ('bob'), ('carol');`
	execOK(ctx, t, pgContainer, "psql", "-U", "postgres", "-d", "testdb", "-c", seedSQL)

	// Step 2 — start the plakar container on the same network (plugin installed by helper).
	plakarContainer := startPlakarContainer(ctx, t, []string{net.Name})

	// Step 3 — initialise a plakar store.
	execOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "create", "-plaintext")

	// Step 5 — run the backup.
	execOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "backup",
		"postgres://postgres:secret@postgres/testdb",
	)

	// Step 6 — list snapshots and extract the snapshot ID.
	lsOut := execCapture(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "ls")
	t.Log("=== plakar snapshots ===")
	t.Log(lsOut)

	// The first token on the first line is the short snapshot ID.
	lines := strings.Split(lsOut, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		t.Fatal("no snapshots found after backup")
	}
	fields := strings.Fields(lines[0])
	if len(fields) == 0 {
		t.Fatalf("unexpected snapshots output: %q", lines[0])
	}
	snapshotID := fields[1]
	t.Logf("snapshot ID: %s", snapshotID)

	// Step 7 — list the snapshot contents.
	t.Log("=== plakar ls snapshot ===")

	execOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "ls", snapshotID)

	// Step 8 — display the manifest.
	t.Log("=== /manifest.json ===")
	execOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "cat", snapshotID+":/manifest.json")
}
