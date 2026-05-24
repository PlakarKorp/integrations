package testhelpers

import (
	"context"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// Snapshot represents a single entry returned by `plakar at <store> ls`.
type Snapshot struct {
	ID string
}

// ListSnapshots runs `plakar at <store> ls`, logs the output, and returns the
// list of snapshots found in the store.
func ListSnapshots(ctx context.Context, t *testing.T, container testcontainers.Container, store string) []Snapshot {
	t.Helper()
	out := ExecCapture(ctx, t, container, "plakar", "at", store, "ls")
	t.Log("=== plakar ls ===")
	t.Log(out)

	var snapshots []Snapshot
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("unexpected ls output line: %q", line)
		}
		snapshots = append(snapshots, Snapshot{ID: fields[1]})
	}
	return snapshots
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
