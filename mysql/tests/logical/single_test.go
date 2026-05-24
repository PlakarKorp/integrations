package logical

import (
	"context"
	"testing"

	"github.com/PlakarKorp/integration-mysql/tests/testhelpers"
)

func TestSingleDatabaseBackup(t *testing.T) {
	t.Parallel()
	for _, v := range testhelpers.DBVariants {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			runSingleDatabaseBackup(t, v)
		})
	}
}

// runSingleDatabaseBackup verifies the full backup and restore cycle for a
// single-database backup against the given database variant:
//  1. Spin up a database container pre-seeded with test data.
//  2. Spin up a plakar container with the plugin installed.
//  3. Create a plakar store, run a backup, inspect the snapshot.
//  4. Spin up a fresh database restore target.
//  5. Restore the snapshot to the target and verify the data.
func runSingleDatabaseBackup(t *testing.T, v testhelpers.DBVariant) {
	t.Helper()
	ctx := context.Background()

	net := testhelpers.NewNetwork(ctx, t)

	// Step 1 — start source database and seed test data.
	dbContainer := testhelpers.StartDBContainer(ctx, t, net, "db", v)
	testhelpers.SeedDB(ctx, t, dbContainer, v)

	// Step 2 — start the plakar container on the same network.
	plakarContainer := testhelpers.StartPlakarContainer(ctx, t, net, v)

	// Step 3 — initialise a plakar store.
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "create", "-plaintext")

	// Step 4 — run the backup (single database).
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "backup",
		v.Protocol+"://root:secret@db/testdb",
	)

	// Step 5 — inspect the snapshot.
	snapshots := testhelpers.ListSnapshots(ctx, t, plakarContainer, "/var/backups")
	if len(snapshots) == 0 {
		t.Fatal("no snapshots found after backup")
	}
	snapID := snapshots[0].ID
	testhelpers.LsSnapshot(ctx, t, plakarContainer, "/var/backups", snapID)
	testhelpers.CatFile(ctx, t, plakarContainer, "/var/backups", snapID, "/manifest.json")

	// Step 6 — start a fresh restore target and restore the snapshot into it.
	restoreContainer := testhelpers.StartDBContainer(ctx, t, net, "db-restore", v)
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "restore",
		"-to", v.Protocol+"://root:secret@db-restore/testdb",
		snapID,
	)

	// Step 7 — verify the data was restored correctly.
	if n := testhelpers.MustQueryInt(ctx, t, restoreContainer, "root", "secret", "testdb", "SELECT COUNT(*) FROM users"); n != 3 {
		t.Fatalf("expected 3 rows in users after restore, got %d", n)
	}
	if n := testhelpers.MustQueryInt(ctx, t, restoreContainer, "root", "secret", "testdb", "SELECT COUNT(*) FROM orders"); n != 3 {
		t.Fatalf("expected 3 rows in orders after restore, got %d", n)
	}
}
