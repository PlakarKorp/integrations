package pgconn

import "testing"

func TestValidDatabaseName(t *testing.T) {
	ok := []string{"myapp", "my_app", "my-app", "MyApp", "db123", "postgres", "ünïcode"}
	for _, name := range ok {
		if err := ValidDatabaseName(name); err != nil {
			t.Errorf("ValidDatabaseName(%q) = %v, want nil", name, err)
		}
	}

	// libpq reads a dbname containing "=" as a conninfo string, so these
	// redirect the connection -- with PGPASSWORD already in the environment.
	bad := []string{
		"",
		"host=attacker.example dbname=x",
		"dbname=x",
		"myapp host=attacker.example",
		"-h",
		"--help",
		"my app",
		"my\tapp",
		"my\x00app",
	}
	for _, name := range bad {
		if err := ValidDatabaseName(name); err == nil {
			t.Errorf("ValidDatabaseName(%q) = nil, want an error", name)
		}
	}
}

// dumpBaseName strips a numeric prefix and the .dump suffix; make sure the
// hostile shapes it can produce are all caught.
func TestValidDatabaseNameOnDumpFilenames(t *testing.T) {
	for _, name := range []string{
		"host=attacker.example dbname=x",
		"-h attacker.example",
	} {
		if err := ValidDatabaseName(name); err == nil {
			t.Errorf("a dump filename yielding %q was accepted", name)
		}
	}
}
