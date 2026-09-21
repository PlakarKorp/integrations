package main

import (
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/stretchr/testify/require"
)

func TestDestinationSkipRootPermsAndTime(t *testing.T) {
	suite := []struct {
		name                 string
		skipRootPermsAndTime bool
		want                 string
	}{
		{name: "off by default", skipRootPermsAndTime: false, want: ""},
		{name: "on when asked", skipRootPermsAndTime: true, want: "true"},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := map[string]string{
				"location": "fs://" + t.TempDir(),
			}

			_, err := destination(test.skipRootPermsAndTime)(
				t.Context(),
				&connectors.Options{},
				"fs",
				config,
			)
			require.NoError(t, err)
			require.Equal(t, test.want, config["skip_root_perms_and_time"])
		})
	}
}
