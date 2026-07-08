//go:build integration

package importer

import (
	"context"
	"os"
	"testing"
)

func TestIncusSourcePing(t *testing.T) {
	if _, err := os.Stat(defaultSocket); err != nil {
		t.Skip("no incus socket on this machine")
	}
	src, err := newIncusSource(defaultSocket)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
