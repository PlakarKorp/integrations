package testhelpers

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go/network"
)

// NewNetwork creates an isolated Docker network for the duration of the test
// and returns its name.  The network is removed automatically when the test ends.
func NewNetwork(ctx context.Context, t *testing.T) string {
	t.Helper()
	net, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	t.Cleanup(func() { _ = net.Remove(ctx) })
	return net.Name
}
