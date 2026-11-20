package testhelpers

import (
	"context"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// ListSnapshots runs `plakar at <store> ls` and returns the snapshot IDs
// found in the store.
func ListSnapshots(ctx context.Context, t *testing.T, container testcontainers.Container, store string) []string {
	t.Helper()
	out := ExecCapture(ctx, t, container, "plakar", "at", store, "ls")
	t.Log(out)

	var ids []string
	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			t.Fatalf("unexpected ls output line: %q", line)
		}
		ids = append(ids, fields[1])
	}
	return ids
}

// LsSnapshot runs `plakar at <store> ls <snapshotID>` and returns its output.
func LsSnapshot(ctx context.Context, t *testing.T, container testcontainers.Container, store, snapshotID string) string {
	t.Helper()
	out := ExecCapture(ctx, t, container, "plakar", "at", store, "ls", snapshotID)
	t.Log(out)
	return out
}
