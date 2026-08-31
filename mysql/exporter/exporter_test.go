package exporter

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/integrations/mysql/mysqlconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/stretchr/testify/require"
)

// tripwireReader fails the test if the restore ever reads the record body,
// which would mean a subprocess was started for it.
type tripwireReader struct {
	t *testing.T
}

func (r *tripwireReader) Read([]byte) (int, error) {
	require.Fail(r.t, "record body was read: the restore reached the client subprocess")
	return 0, fmt.Errorf("unexpected read")
}

func (r *tripwireReader) Close() error { return nil }

// A record name from the archive becomes the trailing argv element of the
// mysql client, which parses options anywhere in argv. restoreSQL must reject
// such a name before it spawns anything.
func TestRestoreSQLRejectsHostileRecordName(t *testing.T) {
	e := &Exporter{
		conn: mysqlconn.ConnConfig{ClientBin: filepath.Join(t.TempDir(), "no-such-mysql")},
	}

	for _, pathname := range []string{
		"/--host=attacker.example.sql",
		"/-hattacker.example.sql",
		"/--execute=DROP DATABASE prod.sql",
		"/db`; DROP DATABASE prod; --.sql",
	} {
		record := &connectors.Record{Pathname: pathname, Reader: &tripwireReader{t: t}}
		err := e.restoreSQL(context.Background(), record)
		require.ErrorContainsf(t, err, "invalid database name", "restoreSQL(%q)", pathname)
	}
}

// The guard must not reject an ordinary dump name: this one gets past
// validation and fails only on the missing client binary.
func TestRestoreSQLAcceptsOrdinaryRecordName(t *testing.T) {
	e := &Exporter{
		conn: mysqlconn.ConnConfig{ClientBin: filepath.Join(t.TempDir(), "no-such-mysql")},
	}

	record := &connectors.Record{Pathname: "/my_app.sql", Reader: &tripwireReader{t: t}}
	err := e.restoreSQL(context.Background(), record)
	require.Error(t, err, "want the missing-binary error")
	require.NotContains(t, err.Error(), "invalid database name")
}
