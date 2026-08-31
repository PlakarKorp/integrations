package mysqlconn

import (
	"fmt"
	"strings"
	"unicode"
)

// ValidDatabaseName reports whether name is safe to hand to a MySQL client
// tool as a database name.
//
// The mysql client parses options anywhere in argv, including after the
// positional database operand, so a value like "--host=attacker.example"
// overrides the -h that precedes it and authenticates against that host with
// the credentials from --defaults-extra-file.  A backtick would break out of a
// quoted identifier in a statement built around the name.  None of these can
// appear in a real database name.
func ValidDatabaseName(name string) error {
	if name == "" {
		return fmt.Errorf("empty database name")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid database name %q: may not start with a dash", name)
	}
	if strings.ContainsAny(name, "`\x00") {
		return fmt.Errorf("invalid database name %q: may not contain a backtick or NUL", name)
	}
	if strings.ContainsFunc(name, unicode.IsSpace) {
		return fmt.Errorf("invalid database name %q: may not contain whitespace", name)
	}
	return nil
}
