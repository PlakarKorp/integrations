//go:build integration

package install

import (
	"context"
	"testing"

	"github.com/PlakarKorp/integration-postgresql/tests/testhelpers"
)

// TestInstallPlugin verifies that the postgresql integration can be built from
// source and installed into a running plakar instance.
func TestInstallPlugin(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.StartPlakarContainer(ctx, t, nil)

	t.Log("=== plakar pkg list ===")
	testhelpers.ExecOK(ctx, t, container, "plakar", "pkg", "list")
}
