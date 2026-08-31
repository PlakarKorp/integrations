package mysqlconn

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidDatabaseName(t *testing.T) {
	ok := []string{"myapp", "my_app", "my-app", "MyApp", "db123", "mysql"}
	for _, name := range ok {
		require.NoErrorf(t, ValidDatabaseName(name), "ValidDatabaseName(%q)", name)
	}

	// The mysql client parses options anywhere in argv, so a trailing
	// positional starting with "-" overrides the connection flags before it.
	bad := []string{
		"",
		"--host=attacker.example",
		"-hattacker.example",
		"--execute=DROP DATABASE prod",
		"my app",
		"my\tapp",
		"db`; DROP DATABASE prod; --",
		"my\x00app",
	}
	for _, name := range bad {
		require.Errorf(t, ValidDatabaseName(name), "ValidDatabaseName(%q)", name)
	}
}
