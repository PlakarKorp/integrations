package logical

import (
	"context"
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

	// Step 5 — inspect the snapshot.
	snapshots := testhelpers.ListSnapshots(ctx, t, plakarContainer, "/var/backups")
	if len(snapshots) == 0 {
		t.Fatal("no snapshots found after backup")
	}
	testhelpers.LsSnapshot(ctx, t, plakarContainer, "/var/backups", snapshots[0].Id)
	testhelpers.CatFile(ctx, t, plakarContainer, "/var/backups", snapshots[0].Id, "/manifest.json")
}
