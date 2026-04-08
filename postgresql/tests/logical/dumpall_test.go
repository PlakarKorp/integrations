package logical

import (
	"context"
	"strings"
	"testing"

	"github.com/PlakarKorp/integration-postgresql/tests/testhelpers"
)

// TestDumpallBackup verifies the full backup cycle for a pg_dumpall logical
// backup (no database specified in the URI — backs up the entire server):
//  1. Spin up a PostgreSQL container pre-seeded with test data.
//  2. Spin up a plakar container with the plugin installed.
//  3. Create a plakar store, run a backup, inspect the snapshot.
func TestDumpallBackup(t *testing.T) {
	ctx := context.Background()

	net := testhelpers.NewNetwork(ctx, t)

	// Step 1 — start a PostgreSQL container on the network.
	pgContainer := testhelpers.StartPostgresContainer(ctx, t, net)

	// Seed the database with a simple table.
	seedSQL := `CREATE TABLE users (id serial PRIMARY KEY, name text NOT NULL);
INSERT INTO users (name) VALUES ('alice'), ('bob'), ('carol');`
	testhelpers.ExecOK(ctx, t, pgContainer, "psql", "-U", "postgres", "-d", "testdb", "-c", seedSQL)

	// Step 2 — start the plakar container on the same network.
	plakarContainer := testhelpers.StartPlakarContainer(ctx, t, net)

	// Step 3 — initialise a plakar store.
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "create", "-plaintext")

	// Step 4 — run a full-server backup (no database in URI triggers pg_dumpall,
	// producing a single all.sql record in the snapshot).
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "backup",
		"postgres://postgres:secret@postgres",
	)

	// Step 5 — list snapshots and extract the snapshot ID.
	lsOut := testhelpers.ExecCapture(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "ls")
	t.Log("=== plakar snapshots ===")
	t.Log(lsOut)

	lines := strings.Split(lsOut, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		t.Fatal("no snapshots found after backup")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 2 {
		t.Fatalf("unexpected snapshots output: %q", lines[0])
	}
	snapshotID := fields[1]
	t.Logf("snapshot ID: %s", snapshotID)

	// Step 6 — list the snapshot contents (should contain /all.sql and /manifest.json).
	t.Log("=== plakar ls snapshot ===")
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "ls", snapshotID)

	// Step 7 — display the manifest.
	t.Log("=== /manifest.json ===")
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "cat", snapshotID+":/manifest.json")
}
