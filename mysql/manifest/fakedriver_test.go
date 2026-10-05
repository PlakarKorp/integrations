package manifest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
)

const fakeDriverName = "cloudsql-mysql"

// queryRecorder tracks every query string the fake driver was asked to run.
type queryRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (r *queryRecorder) record(q string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, q)
}

func (r *queryRecorder) saw(substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.queries {
		if strings.Contains(q, substr) {
			return true
		}
	}
	return false
}

type fakeRows struct {
	cols []string
	data [][]driver.Value
	idx  int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.idx])
	r.idx++
	return nil
}

type fakeConn struct {
	rec *queryRecorder
}

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("fake driver: Prepare not supported")
}

func (c *fakeConn) Close() error { return nil }

func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fake driver: transactions not supported")
}

// QueryContext answers only the queries Emit is known to issue; anything
// else gets an empty result set so collection steps stay best-effort no-ops.
func (c *fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.record(query)
	switch {
	case strings.Contains(query, "VERSION()"):
		return &fakeRows{cols: []string{"version"}, data: [][]driver.Value{{"8.0.33"}}}, nil
	case strings.Contains(query, "read_only"):
		return &fakeRows{cols: []string{"read_only"}, data: [][]driver.Value{{"0"}}}, nil
	case strings.Contains(query, "mysql.user"):
		return &fakeRows{
			cols: []string{"User", "Host", "plugin", "expired", "locked"},
			data: [][]driver.Value{{"root", "localhost", "mysql_native_password", int64(0), int64(0)}},
		}, nil
	default:
		return &fakeRows{}, nil
	}
}

// fakeDriver is registered once under the "cloudsql-mysql" name openDB uses
// for ConnConfig.SqlCloud; its recorder is swapped per test since subtests
// run sequentially.
type fakeDriver struct {
	mu  sync.Mutex
	rec *queryRecorder
}

func (d *fakeDriver) setRecorder(r *queryRecorder) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rec = r
}

func (d *fakeDriver) Open(name string) (driver.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &fakeConn{rec: d.rec}, nil
}

var sharedFakeDriver = &fakeDriver{}

func init() {
	sql.Register(fakeDriverName, sharedFakeDriver)
}
