package logical

import (
	"context"
	"testing"

	"github.com/PlakarKorp/integration-mysql/tests/testhelpers"
)

func TestAllDatabasesBackup(t *testing.T) {
	t.Parallel()
	for _, v := range testhelpers.DBVariants {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			runAllDatabasesBackup(t, v)
		})
	}
}

// runAllDatabasesBackup verifies the full backup and restore cycle when no
// database is specified (--all-databases mode) against the given variant:
//  1. Spin up a database container with two databases (testdb, seconddb).
//  2. Spin up a plakar container with the plugin installed.
//  3. Create a plakar store and run a full-server backup.
//  4. Spin up a fresh database restore target.
//  5. Restore the snapshot and verify both databases and their data.
func runAllDatabasesBackup(t *testing.T, v testhelpers.DBVariant) {
	t.Helper()
	ctx := context.Background()

	net := testhelpers.NewNetwork(ctx, t)

	// Step 1 — start source database, seed testdb, and create seconddb.
	dbContainer := testhelpers.StartDBContainer(ctx, t, net, "db", v)
	testhelpers.SeedDB(ctx, t, dbContainer, v)

	testhelpers.ExecOK(ctx, t, dbContainer,
		v.CLI, "-uroot", "-psecret",
		"-e", "CREATE DATABASE seconddb",
	)
	testhelpers.ExecOK(ctx, t, dbContainer,
		v.CLI, "-uroot", "-psecret", "seconddb",
		"-e", "CREATE TABLE items (id INT AUTO_INCREMENT PRIMARY KEY, label VARCHAR(255))",
	)
	testhelpers.ExecOK(ctx, t, dbContainer,
		v.CLI, "-uroot", "-psecret", "seconddb",
		"-e", "INSERT INTO items (label) VALUES ('alpha'), ('beta')",
	)

	// Step 2 — start the plakar container on the same network.
	plakarContainer := testhelpers.StartPlakarContainer(ctx, t, net, v)

	// Step 3 — initialise a plakar store.
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "create", "-plaintext")

	// Step 4 — run a full-server backup (no database in URI → --all-databases).
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "backup",
		v.Protocol+"://root:secret@db",
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
		"-to", v.Protocol+"://root:secret@db-restore",
		snapID,
	)

	// Step 7 — verify testdb data.
	if n := testhelpers.MustQueryInt(ctx, t, restoreContainer, "root", "secret", "testdb", "SELECT COUNT(*) FROM users"); n != 3 {
		t.Fatalf("expected 3 rows in testdb.users after restore, got %d", n)
	}

	// Step 8 — verify seconddb data.
	if n := testhelpers.MustQueryInt(ctx, t, restoreContainer, "root", "secret", "seconddb", "SELECT COUNT(*) FROM items"); n != 2 {
		t.Fatalf("expected 2 rows in seconddb.items after restore, got %d", n)
	}
}
