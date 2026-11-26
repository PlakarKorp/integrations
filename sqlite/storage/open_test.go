package storage

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Several processes opening one repository must all succeed.
func TestConcurrentOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.db")
	st, err := NewStore(t.Context(), "sqlite", map[string]string{"location": "sqlite://" + path})
	require.NoError(t, err)
	require.NoError(t, st.Create(t.Context(), []byte("config")))
	require.NoError(t, st.Close(t.Context()))

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			st, err := NewStore(t.Context(), "sqlite", map[string]string{"location": "sqlite://" + path})
			if err != nil {
				errs <- err
				return
			}
			if _, err := st.Open(t.Context()); err != nil {
				errs <- err
				return
			}
			errs <- st.Close(t.Context())
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
