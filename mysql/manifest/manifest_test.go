package manifest

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/PlakarKorp/integrations/mysql/mysqlconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runEmit(t *testing.T, database string) (Manifest, *queryRecorder) {
	t.Helper()
	rec := &queryRecorder{}
	sharedFakeDriver.setRecorder(rec)

	cfg := Config{
		Conn: mysqlconn.ConnConfig{
			Host:     "127.0.0.1",
			Port:     "3306",
			SqlCloud: true,
			TrueHost: "fake-host",
		},
		Flavor:   "mysql",
		Database: database,
	}

	records := make(chan *connectors.Record, 1)
	err := Emit(t.Context(), cfg, records)
	require.NoError(t, err)

	got := <-records
	data, err := io.ReadAll(got.Reader)
	require.NoError(t, err)

	var m Manifest
	require.NoError(t, json.Unmarshal(data, &m))
	return m, rec
}

func TestEmitSkipsUsersForSingleDatabase(t *testing.T) {
	m, rec := runEmit(t, "mydb")

	assert.False(t, rec.saw("mysql.user"), "collectUsers must not run for a single-database backup")
	assert.Empty(t, m.Users)
	assert.Equal(t, "mydb", m.Database)
}

func TestEmitCollectsUsersForWholeServer(t *testing.T) {
	m, rec := runEmit(t, "")

	assert.True(t, rec.saw("mysql.user"), "collectUsers must run for a whole-server backup")
	require.Len(t, m.Users, 1)
	assert.Equal(t, "root", m.Users[0].User)
}
