package testhelpers

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartPostgresContainer starts a postgres:17 container attached to net with
// the given network alias, a default database named "testdb" (password "secret").
//
// The server is configured with wal_level=replica and a pg_hba.conf rule that
// allows replication connections from any host, so both logical and physical
// (pg_basebackup) backups work against the same container.
//
// The container is automatically terminated when the test ends.
func StartPostgresContainer(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork, alias string) testcontainers.Container {
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
		Networks:       []string{net.Name},
		NetworkAliases: map[string][]string{net.Name: {alias}},
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
