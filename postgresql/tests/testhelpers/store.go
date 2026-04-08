package testhelpers

import (
	"context"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// FirstSnapshotID runs `plakar at <store> ls`, logs the output, and returns
// the ID of the first snapshot.  The test fails if no snapshots are found.
func FirstSnapshotID(ctx context.Context, t *testing.T, container testcontainers.Container, store string) string {
	t.Helper()
	out := ExecCapture(ctx, t, container, "plakar", "at", store, "ls")
	t.Log("=== plakar ls ===")
	t.Log(out)

	lines := strings.Split(out, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		t.Fatal("no snapshots found")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 2 {
		t.Fatalf("unexpected ls output: %q", lines[0])
	}
	id := fields[1]
	t.Logf("snapshot ID: %s", id)
	return id
}

// LsSnapshot runs `plakar at <store> ls <snapshotID>` and logs the output.
func LsSnapshot(ctx context.Context, t *testing.T, container testcontainers.Container, store, snapshotID string) {
	t.Helper()
	t.Log("=== plakar ls snapshot ===")
	ExecOK(ctx, t, container, "plakar", "at", store, "ls", snapshotID)
}

// CatFile runs `plakar at <store> cat <snapshotID>:<path>` and logs the output.
func CatFile(ctx context.Context, t *testing.T, container testcontainers.Container, store, snapshotID, path string) {
	t.Helper()
	t.Logf("=== %s ===", path)
	ExecOK(ctx, t, container, "plakar", "at", store, "cat", snapshotID+":"+path)
}
