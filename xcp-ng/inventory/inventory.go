package inventory

import (
	"context"

	"github.com/PlakarKorp/integration-sdk"
)

type xcpng struct{}

func NewInventory(ctx context.Context, params map[string]string) (integration.Inventory, error) {
	return &xcpng{}, nil
}

func (x *xcpng) List(ctx context.Context, resources chan<- *integration.InventoryEntry) error {
	defer close(resources)
	return nil
}

func (x *xcpng) Close(ctx context.Context) error { return nil }
