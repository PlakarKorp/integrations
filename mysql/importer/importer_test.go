package importer

import (
	"testing"

	"github.com/PlakarKorp/integrations/mysql/mysqlconn"
	"github.com/stretchr/testify/require"
)

// Database is appended as the trailing argv element of the dump command, so a
// hostile value must be rejected at construction, whether it comes from the
// database key or from the path of the location URI.
func TestNewRejectsHostileDatabase(t *testing.T) {
	configs := []map[string]string{
		{"database": "--host=attacker.example"},
		{"database": "-hattacker.example"},
		{"location": "mysql://127.0.0.1:3306/--host=attacker.example"},
	}
	for _, config := range configs {
		_, err := New("mysql", mysqlconn.ConnConfig{}, config)
		require.ErrorContainsf(t, err, "invalid database name", "New(%v)", config)
	}
}

func TestNewAcceptsOrdinaryDatabase(t *testing.T) {
	imp, err := New("mysql", mysqlconn.ConnConfig{}, map[string]string{"database": "my_app"})
	require.NoError(t, err)
	require.Equal(t, "my_app", imp.Database)
}
