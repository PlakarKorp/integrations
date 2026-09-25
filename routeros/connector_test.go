package routeros

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRedactsCredentials(t *testing.T) {
	loc := "routeros+export://user:secret@host/%zz"
	_, err := NewImporter(context.Background(), nil, "routeros+export",
		map[string]string{"location": loc})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")
}
