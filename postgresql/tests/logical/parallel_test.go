package logical

import (
	"context"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/PlakarKorp/integrations/postgresql/tests/testhelpers"
)

// TestParallelLogicalBackup verifies a format=directory backup: pg_dump -Fd -j
// writes through a FUSE mount and each file lands in the snapshot as written.
//  1. Seed a database with a partitioned table and a large object.
//  2. Back it up twice with 4 parallel dumps and -cache no: the second run
//     covers a backup with a previous snapshot of the same origin.
//  3. In each snapshot, compare every file except toc.dat (it holds a
//     timestamp) with a pg_dump -Fd taken directly to local disk.
func TestParallelLogicalBackup(t *testing.T) {
	ctx := context.Background()

	net := testhelpers.NewNetwork(ctx, t)

	pgContainer := testhelpers.StartPostgresContainer(ctx, t, net, "postgres")
	seedSQL := `CREATE TABLE events (id int NOT NULL, payload text) PARTITION BY RANGE (id);
CREATE TABLE events_0 PARTITION OF events FOR VALUES FROM (0) TO (10000);
CREATE TABLE events_1 PARTITION OF events FOR VALUES FROM (10000) TO (20000);
CREATE TABLE events_2 PARTITION OF events FOR VALUES FROM (20000) TO (30000);
CREATE TABLE events_3 PARTITION OF events FOR VALUES FROM (30000) TO (40000);
INSERT INTO events SELECT i, md5(i::text) FROM generate_series(0, 39999) i;
CREATE TABLE users (id serial PRIMARY KEY, name text NOT NULL);
INSERT INTO users (name) VALUES ('alice'), ('bob'), ('carol');
SELECT lo_from_bytea(0, 'large object');`
	testhelpers.ExecOK(ctx, t, pgContainer, "psql", "-U", "postgres", "-d", "testdb", "-c", seedSQL)

	plakarContainer := testhelpers.StartPlakarFUSEContainer(ctx, t, net)
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "create", "-plaintext")

	for range 2 {
		testhelpers.ExecOK(ctx, t, plakarContainer,
			"plakar", "at", "/var/backups", "backup", "-cache", "no", "-o", "format=directory", "-o", "parallel_dumps=4",
			"postgres://postgres:secret@postgres/testdb",
		)
	}

	testhelpers.ExecOK(ctx, t, plakarContainer, "bash", "-c",
		"PGPASSWORD=secret pg_dump -h postgres -U postgres -Fd -Z0 -f /tmp/ref testdb")
	ref := strings.Fields(testhelpers.ExecCapture(ctx, t, plakarContainer, "ls", "/tmp/ref"))
	slices.Sort(ref)

	snapshots := testhelpers.ListSnapshots(ctx, t, plakarContainer, "/var/backups")
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}
	for _, snap := range snapshots {
		var files []string
		for _, line := range strings.Split(testhelpers.LsSnapshotOutput(ctx, t, plakarContainer, "/var/backups", snap.ID+":/00001-testdb"), "\n") {
			if fields := strings.Fields(line); len(fields) > 0 {
				files = append(files, path.Base(fields[len(fields)-1]))
			}
		}
		slices.Sort(files)
		if !slices.Equal(files, ref) {
			t.Errorf("snapshot %s: files %v, want %v", snap.ID, files, ref)
			continue
		}
		// toc.dat holds a timestamp: check it with pg_restore instead.
		testhelpers.ExecOK(ctx, t, plakarContainer, "bash", "-c", `set -eo pipefail
rm -rf /tmp/snap && mkdir /tmp/snap
for f in /tmp/ref/*; do
	name=$(basename "$f")
	plakar at /var/backups cat "$0:/00001-testdb/$name" > "/tmp/snap/$name"
	[ "$name" = toc.dat ] || cmp "/tmp/snap/$name" "$f"
done
pg_restore -l /tmp/snap > /dev/null`, snap.ID)
	}
}

// TestParallelBackupWithLargeObjects verifies that a format=directory backup of a
// database with many large objects completes with a single plakar worker.
// pg_dump keeps blobs_<id>.toc open while it writes the blob files it lists,
// and its stdio buffer flushes the TOC long before it closes it: streamed, the
// TOC would hold the only worker that the next blob file needs.
//  1. Seed 2000 large objects: two TOCs of 1000 lines, each well past the
//     stdio buffer.
//  2. Back up with -concurrency 1 and 4 parallel dumps, under a timeout so that a
//     deadlock fails the test instead of hanging it.
//  3. Restore the files to disk and check pg_restore reads every blob.
func TestParallelBackupWithLargeObjects(t *testing.T) {
	ctx := context.Background()

	net := testhelpers.NewNetwork(ctx, t)

	pgContainer := testhelpers.StartPostgresContainer(ctx, t, net, "postgres")
	testhelpers.ExecOK(ctx, t, pgContainer, "psql", "-U", "postgres", "-d", "testdb", "-c",
		"SELECT lo_from_bytea(0, convert_to(i::text, 'UTF8')) FROM generate_series(1, 2000) i")

	plakarContainer := testhelpers.StartPlakarFUSEContainer(ctx, t, net)
	testhelpers.ExecOK(ctx, t, plakarContainer, "plakar", "at", "/var/backups", "create", "-plaintext")
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"timeout", "300",
		"plakar", "-concurrency", "1", "at", "/var/backups", "backup", "-o", "format=directory", "-o", "parallel_dumps=4",
		"postgres://postgres:secret@postgres/testdb",
	)

	snapshots := testhelpers.ListSnapshots(ctx, t, plakarContainer, "/var/backups")
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	testhelpers.ExecOK(ctx, t, plakarContainer,
		"plakar", "at", "/var/backups", "restore", "-to", "/tmp/restore", snapshots[0].ID)

	blobs := testhelpers.ExecCapture(ctx, t, plakarContainer, "bash", "-c",
		"ls /tmp/restore/00001-testdb | grep -c '^blob_.*\\.dat$'")
	if blobs != "2000" {
		t.Errorf("expected 2000 blob files, got %s", blobs)
	}
	testhelpers.ExecOK(ctx, t, plakarContainer, "pg_restore", "-l", "/tmp/restore/00001-testdb")
}
