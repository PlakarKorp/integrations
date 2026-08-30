package mysqlconn

import "testing"

func TestValidDatabaseName(t *testing.T) {
	ok := []string{"myapp", "my_app", "my-app", "MyApp", "db123", "mysql"}
	for _, name := range ok {
		if err := ValidDatabaseName(name); err != nil {
			t.Errorf("ValidDatabaseName(%q) = %v, want nil", name, err)
		}
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
		if err := ValidDatabaseName(name); err == nil {
			t.Errorf("ValidDatabaseName(%q) = nil, want an error", name)
		}
	}
}
