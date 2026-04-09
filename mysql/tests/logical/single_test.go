package logical

import (
	"context"
	"testing"

	"github.com/PlakarKorp/integration-mysql/tests/testhelpers"
)

// TestSingleDatabaseBackup verifies the full backup and restore cycle for a
// logical (mysql://) single-database backup:
//  1. Spin up a MySQL container pre-seeded with test data.
//  2. Spin up a plakar container with the plugin installed.
//  3. Create a plakar store, run a backup, inspect the snapshot.
//  4. Spin up a fresh MySQL restore target.
//  5. Restore the snapshot to the target and verify the data.
func TestSingleDatabaseBackup(t *testing.T) {
	ctx := context.Background()

	net := testhelpers.NewNetwork(ctx, t)

	// Step 1 — start source MySQL and seed test data.
	mysqlContainer := testhelpers.StartMySQLContainer(ctx, t, net, "mysql")
	testhelpers.SeedMySQL(ctx, t, mysqlContainer)

	// Step 2 — start the plakar container on the same network.
	plakarContainer := testhelpers.StartPlakarContainer(ctx, t, net)

	// Step 3 — initialise a plakar store.
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "create", "-plaintext")

	// Step 4 — run the backup (single database).
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "backup",
		"mysql://root:secret@mysql/testdb",
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
	// The MySQL container creates testdb automatically via MYSQL_DATABASE env,
	// so no create_db=true is needed.
	restoreContainer := testhelpers.StartMySQLContainer(ctx, t, net, "mysql-restore")
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "restore",
		"-to", "mysql://root:secret@mysql-restore/testdb",
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
