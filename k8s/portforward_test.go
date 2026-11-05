package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWaitForwardReady(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	require.NoError(t, waitForward(t.Context(), ready, make(chan error)))
}

func TestWaitForwardFailsToStart(t *testing.T) {
	cause := errors.New("pods/portforward is forbidden")
	errc := make(chan error, 1)
	errc <- cause
	require.ErrorIs(t, waitForward(t.Context(), make(chan struct{}), errc), cause)
}

func TestWaitForwardEndsBeforeReady(t *testing.T) {
	errc := make(chan error, 1)
	errc <- nil
	require.Error(t, waitForward(t.Context(), make(chan struct{}), errc))
}

func TestWaitForwardCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, waitForward(ctx, make(chan struct{}), make(chan error)), context.Canceled)
}
