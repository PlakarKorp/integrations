package logical

import (
	"context"
	"strings"
	"testing"

	"github.com/PlakarKorp/integration-postgresql/tests/testhelpers"
)

// TestDumpallBackup verifies the full backup and restore cycle for an
// all-databases logical backup (no database specified in the URI):
//  1. Spin up a source PostgreSQL container pre-seeded with test data.
//  2. Spin up a plakar container with the plugin installed.
//  3. Create a plakar store, run a backup, inspect the snapshot.
//  4. Spin up a restore target PostgreSQL container.
//  5. Restore the snapshot to the target and verify the data landed correctly.
//
// The backup produces one record per database in pg_dump custom format
// (e.g. /00001-postgres.dump, /00002-testdb.dump) plus a
// /00000-globals.sql record containing roles and tablespaces.
func TestDumpallBackup(t *testing.T) {
	ctx := context.Background()

	net := testhelpers.NewNetwork(ctx, t)

	// Step 1 — start the source PostgreSQL container and seed it.
	pgContainer := testhelpers.StartPostgresContainer(ctx, t, net, "postgres")
	seedSQL := `CREATE TABLE users (id serial PRIMARY KEY, name text NOT NULL);
INSERT INTO users (name) VALUES ('alice'), ('bob'), ('carol');`
	testhelpers.ExecOK(ctx, t, pgContainer, "psql", "-U", "postgres", "-d", "testdb", "-c", seedSQL)

	// Step 2 — start the plakar container on the same network.
	plakarContainer := testhelpers.StartPlakarContainer(ctx, t, net)

	// Step 3 — initialise a plakar store.
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "create", "-plaintext")

	// Step 4 — run a full-server backup (no database in URI triggers a per-database
	// backup, producing 00000-globals.sql + one .dump file per database).
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "backup",
		"postgres://postgres:secret@postgres",
	)

	// Step 5 — inspect the snapshot and assert the expected file layout.
	snapshots := testhelpers.ListSnapshots(ctx, t, plakarContainer, "/var/backups")
	if len(snapshots) == 0 {
		t.Fatal("no snapshots found after backup")
	}
	snap := snapshots[0].ID
	files := testhelpers.LsSnapshotOutput(ctx, t, plakarContainer, "/var/backups", snap)
	testhelpers.CatFile(ctx, t, plakarContainer, "/var/backups", snap, "/manifest.json")

	// Globals and at least two per-database dumps (postgres + testdb) must be present.
	for _, want := range []string{"00000-globals.sql", "testdb.dump"} {
		if !strings.Contains(files, want) {
			t.Errorf("snapshot missing expected file %q", want)
		}
	}

	// Step 6 — start a fresh restore target and restore the snapshot into it.
	restoreContainer := testhelpers.StartPostgresContainer(ctx, t, net, "postgres-restore")
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "restore", "-to", "postgres://postgres:secret@postgres-restore", snap)

	// Step 7 — verify the data was restored correctly.
	out := testhelpers.ExecCapture(ctx, t, restoreContainer,
		"psql", "-U", "postgres", "-d", "testdb", "-t", "-c", "SELECT count(*) FROM users")
	if strings.TrimSpace(out) != "3" {
		t.Fatalf("expected 3 rows after restore, got %q", out)
	}
}
