package physical

import (
	"context"
	"testing"

	"github.com/PlakarKorp/integration-postgresql/tests/testhelpers"
)

// TestPhysicalBackup verifies the full backup cycle for a physical
// (postgres+bin://) backup using pg_basebackup:
//  1. Spin up a PostgreSQL container configured for replication.
//  2. Spin up a plakar container with the plugin installed.
//  3. Create a plakar store, run a physical backup, inspect the snapshot.
func TestPhysicalBackup(t *testing.T) {
	ctx := context.Background()

	net := testhelpers.NewNetwork(ctx, t)

	// Step 1 — start a PostgreSQL container with replication enabled.
	testhelpers.StartPostgresContainer(ctx, t, net, "postgres")

	// Step 2 — start the plakar container on the same network.
	plakarContainer := testhelpers.StartPlakarContainer(ctx, t, net)

	// Step 3 — initialise a plakar store.
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "create", "-plaintext")

	// Step 4 — run the physical backup.
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "backup",
		"postgres+bin://postgres:secret@postgres",
	)

	// Step 5 — inspect the snapshot.
	snapshots := testhelpers.ListSnapshots(ctx, t, plakarContainer, "/var/backups")
	if len(snapshots) == 0 {
		t.Fatal("no snapshots found after backup")
	}
	testhelpers.LsSnapshot(ctx, t, plakarContainer, "/var/backups", snapshots[0].Id)
	testhelpers.CatFile(ctx, t, plakarContainer, "/var/backups", snapshots[0].Id, "/manifest.json")
}
