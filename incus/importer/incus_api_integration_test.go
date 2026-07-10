//go:build integration

package importer

import (
	"context"
	"os"
	"testing"

	"github.com/PlakarKorp/integration-incus/internal/conn"
)

func TestIncusSourcePing(t *testing.T) {
	if _, err := os.Stat(conn.DefaultSocket); err != nil {
		t.Skip("no incus socket on this machine")
	}
	src, err := newIncusSource(map[string]string{"socket": conn.DefaultSocket}, defaultBackupTTL, defaultCleanupTimeout)
	if err != nil {
		t.Skipf("incus socket present but unusable: %v", err)
	}
	if err := src.Ping(context.Background()); err != nil {
		t.Skipf("incus daemon unreachable: %v", err)
	}
}
