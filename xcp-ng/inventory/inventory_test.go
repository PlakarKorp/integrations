package inventory

import (
	"context"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/require"
)

func TestListClosesChannel(t *testing.T) {
	ctx := context.Background()
	inv, err := NewInventory(ctx, map[string]string{})
	require.NoError(t, err)

	resources := make(chan *integration.InventoryEntry)
	errc := make(chan error, 1)
	go func() { errc <- inv.List(ctx, resources) }()

	var entries []*integration.InventoryEntry
	for e := range resources {
		entries = append(entries, e)
	}
	require.Empty(t, entries)
	require.NoError(t, <-errc)
	require.NoError(t, inv.Close(ctx))
}
