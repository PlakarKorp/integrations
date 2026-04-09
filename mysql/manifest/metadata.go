package manifest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-mysql/mysqlconn"
	_ "github.com/go-sql-driver/mysql"
)

// --- Types ---

// ServerConfig holds key MySQL server configuration parameters.
type ServerConfig struct {
	DataDir              string `json:"datadir,omitempty"`
	Hostname             string `json:"hostname,omitempty"`
	Port                 string `json:"port,omitempty"`
	CharacterSetServer   string `json:"character_set_server,omitempty"`
	CollationServer      string `json:"collation_server,omitempty"`
	InnoDBBufferPoolSize int64  `json:"innodb_buffer_pool_size,omitempty"`
	MaxConnections       int    `json:"max_connections,omitempty"`
	SQLMode              string `json:"sql_mode,omitempty"`
	TimeZone             string `json:"time_zone,omitempty"`
	GTIDMode             string `json:"gtid_mode,omitempty"`
	BinlogFormat         string `json:"binlog_format,omitempty"`
	InnoDBVersion        string `json:"innodb_version,omitempty"`
}

// UserInfo describes a MySQL user account.
type UserInfo struct {
	User               string `json:"user"`
	Host               string `json:"host"`
	AuthPlugin         string `json:"auth_plugin,omitempty"`
	PasswordExpired    bool   `json:"password_expired,omitempty"`
	AccountLocked      bool   `json:"account_locked,omitempty"`
}

// ColumnInfo describes a single column in a table.
type ColumnInfo struct {
	Position  int    `json:"position"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Nullable  bool   `json:"nullable"`
	Default   string `json:"default,omitempty"`
	Extra     string `json:"extra,omitempty"` // e.g. "auto_increment", "on update CURRENT_TIMESTAMP"
	CharSet   string `json:"character_set,omitempty"`
	Collation string `json:"collation,omitempty"`
}

// IndexInfo describes an index on a table.
type IndexInfo struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	NonUnique  bool     `json:"non_unique"`
	IndexType  string   `json:"index_type"`  // BTREE, HASH, FULLTEXT, SPATIAL
}

// ConstraintInfo describes a table constraint.
type ConstraintInfo struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"` // PRIMARY KEY, UNIQUE, FOREIGN KEY, CHECK
	Columns        []string `json:"columns,omitempty"`
	ReferencedTable string  `json:"referenced_table,omitempty"`
}

// TableInfo describes a table or view in a database.
type TableInfo struct {
	Name          string           `json:"name"`
	Type          string           `json:"type"`           // BASE TABLE, VIEW, SYSTEM VIEW
	Engine        string           `json:"engine,omitempty"`
	RowFormat     string           `json:"row_format,omitempty"`
	RowEstimate   int64            `json:"row_estimate,omitempty"`
	DataLength    int64            `json:"data_length,omitempty"`
	IndexLength   int64            `json:"index_length,omitempty"`
	AutoIncrement int64            `json:"auto_increment,omitempty"`
	CharSet       string           `json:"character_set,omitempty"`
	Collation     string           `json:"collation,omitempty"`
	CreateTime    *time.Time       `json:"create_time,omitempty"`
	UpdateTime    *time.Time       `json:"update_time,omitempty"`
	Comment       string           `json:"comment,omitempty"`
	Columns       []ColumnInfo     `json:"columns,omitempty"`
	Indexes       []IndexInfo      `json:"indexes,omitempty"`
	Constraints   []ConstraintInfo `json:"constraints,omitempty"`
}

// RoutineInfo describes a stored procedure or function.
type RoutineInfo struct {
	Name            string `json:"name"`
	Type            string `json:"type"` // PROCEDURE or FUNCTION
	Definer         string `json:"definer,omitempty"`
	SQLMode         string `json:"sql_mode,omitempty"`
	IsDeterministic bool   `json:"is_deterministic"`
	SecurityType    string `json:"security_type,omitempty"` // DEFINER or INVOKER
	Created         string `json:"created,omitempty"`
	Modified        string `json:"modified,omitempty"`
}

// TriggerInfo describes a table trigger.
type TriggerInfo struct {
	Name        string `json:"name"`
	Event       string `json:"event"`   // INSERT, UPDATE, DELETE
	Timing      string `json:"timing"`  // BEFORE, AFTER
	Table       string `json:"table"`
	Definer     string `json:"definer,omitempty"`
	SQLMode     string `json:"sql_mode,omitempty"`
	Created     string `json:"created,omitempty"`
}

// EventInfo describes a MySQL event scheduler event.
type EventInfo struct {
	Name          string `json:"name"`
	Definer       string `json:"definer,omitempty"`
	Status        string `json:"status,omitempty"` // ENABLED, DISABLED, SLAVESIDE_DISABLED
	ExecuteAt     string `json:"execute_at,omitempty"`
	IntervalValue string `json:"interval_value,omitempty"`
	IntervalField string `json:"interval_field,omitempty"`
	Starts        string `json:"starts,omitempty"`
	Ends          string `json:"ends,omitempty"`
}

// DatabaseInfo describes a MySQL database (schema) and its contents.
type DatabaseInfo struct {
	Name      string        `json:"name"`
	CharSet   string        `json:"character_set,omitempty"`
	Collation string        `json:"collation,omitempty"`
	Tables    []TableInfo   `json:"tables,omitempty"`
	Routines  []RoutineInfo `json:"routines,omitempty"`
	Triggers  []TriggerInfo `json:"triggers,omitempty"`
	Events    []EventInfo   `json:"events,omitempty"`
}

// --- Helpers ---

func openDB(conn mysqlconn.ConnConfig, database string) (*sql.DB, error) {
	db, err := sql.Open("mysql", conn.DSN(database))
	if err != nil {
		return nil, fmt.Errorf("opening connection to %s: %w", conn.Host, err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func nullableString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func nullableTime(nt sql.NullTime) *time.Time {
	if nt.Valid {
		t := nt.Time
		return &t
	}
	return nil
}

func nullableInt64(ni sql.NullInt64) int64 {
	if ni.Valid {
		return ni.Int64
	}
	return 0
}


// collectServerConfig queries key server variables from INFORMATION_SCHEMA / SHOW VARIABLES.
func collectServerConfig(ctx context.Context, db *sql.DB) (ServerConfig, error) {
	query := `SELECT VARIABLE_NAME, VARIABLE_VALUE
FROM performance_schema.global_variables
WHERE VARIABLE_NAME IN (
  'datadir','hostname','port',
  'character_set_server','collation_server',
  'innodb_buffer_pool_size','max_connections',
  'sql_mode','time_zone','gtid_mode',
  'binlog_format','innodb_version'
)
ORDER BY VARIABLE_NAME`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		// Fallback: performance_schema may not be available on all setups.
		return collectServerConfigFallback(ctx, db)
	}
	defer rows.Close()

	var cfg ServerConfig
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			continue
		}
		switch name {
		case "datadir":
			cfg.DataDir = value
		case "hostname":
			cfg.Hostname = value
		case "port":
			cfg.Port = value
		case "character_set_server":
			cfg.CharacterSetServer = value
		case "collation_server":
			cfg.CollationServer = value
		case "innodb_buffer_pool_size":
			var v int64
			fmt.Sscanf(value, "%d", &v)
			cfg.InnoDBBufferPoolSize = v
		case "max_connections":
			var v int
			fmt.Sscanf(value, "%d", &v)
			cfg.MaxConnections = v
		case "sql_mode":
			cfg.SQLMode = value
		case "time_zone":
			cfg.TimeZone = value
		case "gtid_mode":
			cfg.GTIDMode = value
		case "binlog_format":
			cfg.BinlogFormat = value
		case "innodb_version":
			cfg.InnoDBVersion = value
		}
	}
	return cfg, rows.Err()
}

func collectServerConfigFallback(ctx context.Context, db *sql.DB) (ServerConfig, error) {
	rows, err := db.QueryContext(ctx, "SHOW GLOBAL VARIABLES WHERE Variable_name IN "+
		"('datadir','hostname','port','character_set_server','collation_server',"+
		"'innodb_buffer_pool_size','max_connections','sql_mode','time_zone',"+
		"'gtid_mode','binlog_format','innodb_version')")
	if err != nil {
		return ServerConfig{}, fmt.Errorf("SHOW GLOBAL VARIABLES: %w", err)
	}
	defer rows.Close()

	var cfg ServerConfig
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			continue
		}
		switch name {
		case "datadir":
			cfg.DataDir = value
		case "hostname":
			cfg.Hostname = value
		case "port":
			cfg.Port = value
		case "character_set_server":
			cfg.CharacterSetServer = value
		case "collation_server":
			cfg.CollationServer = value
		case "innodb_buffer_pool_size":
			var v int64
			fmt.Sscanf(value, "%d", &v)
			cfg.InnoDBBufferPoolSize = v
		case "max_connections":
			var v int
			fmt.Sscanf(value, "%d", &v)
			cfg.MaxConnections = v
		case "sql_mode":
			cfg.SQLMode = value
		case "time_zone":
			cfg.TimeZone = value
		case "gtid_mode":
			cfg.GTIDMode = value
		case "binlog_format":
			cfg.BinlogFormat = value
		case "innodb_version":
			cfg.InnoDBVersion = value
		}
	}
	return cfg, rows.Err()
}

// collectUsers queries the mysql.user table (best-effort).
func collectUsers(ctx context.Context, db *sql.DB) ([]UserInfo, error) {
	rows, err := db.QueryContext(ctx, `
SELECT User, Host,
  COALESCE(plugin, ''),
  IF(password_expired = 'Y', 1, 0),
  IF(account_locked = 'Y', 1, 0)
FROM mysql.user
ORDER BY User, Host`)
	if err != nil {
		return nil, fmt.Errorf("querying mysql.user: %w", err)
	}
	defer rows.Close()

	var users []UserInfo
	for rows.Next() {
		var u UserInfo
		var expired, locked int
		if err := rows.Scan(&u.User, &u.Host, &u.AuthPlugin, &expired, &locked); err != nil {
			continue
		}
		u.PasswordExpired = expired == 1
		u.AccountLocked = locked == 1
		users = append(users, u)
	}
	return users, rows.Err()
}

// collectDatabases lists all user databases (excludes performance_schema, sys,
// information_schema, mysql unless they are the backup target).
func collectDatabases(ctx context.Context, db *sql.DB, targetDB string) ([]DatabaseInfo, error) {
	var query string
	var args []interface{}
	if targetDB != "" {
		query = `SELECT SCHEMA_NAME, DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME
FROM information_schema.SCHEMATA
WHERE SCHEMA_NAME = ?
ORDER BY SCHEMA_NAME`
		args = []interface{}{targetDB}
	} else {
		query = `SELECT SCHEMA_NAME, DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME
FROM information_schema.SCHEMATA
WHERE SCHEMA_NAME NOT IN ('information_schema','performance_schema','sys','mysql')
ORDER BY SCHEMA_NAME`
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying SCHEMATA: %w", err)
	}
	defer rows.Close()

	var dbs []DatabaseInfo
	for rows.Next() {
		var d DatabaseInfo
		if err := rows.Scan(&d.Name, &d.CharSet, &d.Collation); err != nil {
			continue
		}
		dbs = append(dbs, d)
	}
	return dbs, rows.Err()
}

// collectDatabaseDetail populates tables, routines, triggers, and events for a database.
// Individual query failures are non-fatal.
func collectDatabaseDetail(ctx context.Context, db *sql.DB, info *DatabaseInfo) {
	if tables, err := queryTables(ctx, db, info.Name); err == nil {
		info.Tables = tables
	}
	if routines, err := queryRoutines(ctx, db, info.Name); err == nil {
		info.Routines = routines
	}
	if triggers, err := queryTriggers(ctx, db, info.Name); err == nil {
		info.Triggers = triggers
	}
	if events, err := queryEvents(ctx, db, info.Name); err == nil {
		info.Events = events
	}
}

func queryTables(ctx context.Context, db *sql.DB, schema string) ([]TableInfo, error) {
	// CHARACTER_SET_NAME was removed from information_schema.TABLES in MySQL 8.0.
	// Derive it from the collation via COLLATION_CHARACTER_SET_APPLICABILITY instead.
	rows, err := db.QueryContext(ctx, `
SELECT
  t.TABLE_NAME,
  t.TABLE_TYPE,
  COALESCE(t.ENGINE, ''),
  COALESCE(t.ROW_FORMAT, ''),
  COALESCE(t.TABLE_ROWS, 0),
  COALESCE(t.DATA_LENGTH, 0),
  COALESCE(t.INDEX_LENGTH, 0),
  COALESCE(t.AUTO_INCREMENT, 0),
  COALESCE(ccsa.CHARACTER_SET_NAME, ''),
  COALESCE(t.TABLE_COLLATION, ''),
  t.CREATE_TIME,
  t.UPDATE_TIME,
  COALESCE(t.TABLE_COMMENT, '')
FROM information_schema.TABLES t
LEFT JOIN information_schema.COLLATION_CHARACTER_SET_APPLICABILITY ccsa
  ON ccsa.COLLATION_NAME = t.TABLE_COLLATION
WHERE t.TABLE_SCHEMA = ?
ORDER BY t.TABLE_NAME`, schema)
	if err != nil {
		return nil, fmt.Errorf("querying TABLES for %s: %w", schema, err)
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var t TableInfo
		var createTime sql.NullTime
		var updateTime sql.NullTime
		if err := rows.Scan(
			&t.Name, &t.Type, &t.Engine, &t.RowFormat,
			&t.RowEstimate, &t.DataLength, &t.IndexLength, &t.AutoIncrement,
			&t.CharSet, &t.Collation,
			&createTime, &updateTime,
			&t.Comment,
		); err != nil {
			continue
		}
		t.CreateTime = nullableTime(createTime)
		t.UpdateTime = nullableTime(updateTime)
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Enrich each table with columns, indexes, and constraints.
	tableIdx := make(map[string]int, len(tables))
	for i, t := range tables {
		tableIdx[t.Name] = i
	}

	if err := queryColumns(ctx, db, schema, tables, tableIdx); err == nil {
		// columns populated
	}
	if err := queryIndexes(ctx, db, schema, tables, tableIdx); err == nil {
		// indexes populated
	}
	if err := queryConstraints(ctx, db, schema, tables, tableIdx); err == nil {
		// constraints populated
	}

	return tables, nil
}

func queryColumns(ctx context.Context, db *sql.DB, schema string, tables []TableInfo, tableIdx map[string]int) error {
	rows, err := db.QueryContext(ctx, `
SELECT
  TABLE_NAME,
  ORDINAL_POSITION,
  COLUMN_NAME,
  COLUMN_TYPE,
  IF(IS_NULLABLE = 'YES', 1, 0),
  COALESCE(COLUMN_DEFAULT, ''),
  COALESCE(EXTRA, ''),
  COALESCE(CHARACTER_SET_NAME, ''),
  COALESCE(COLLATION_NAME, '')
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = ?
ORDER BY TABLE_NAME, ORDINAL_POSITION`, schema)
	if err != nil {
		return fmt.Errorf("querying COLUMNS for %s: %w", schema, err)
	}
	defer rows.Close()

	for rows.Next() {
		var tableName string
		var col ColumnInfo
		var nullable int
		if err := rows.Scan(
			&tableName, &col.Position, &col.Name, &col.Type,
			&nullable, &col.Default, &col.Extra, &col.CharSet, &col.Collation,
		); err != nil {
			continue
		}
		col.Nullable = nullable == 1
		if idx, ok := tableIdx[tableName]; ok {
			tables[idx].Columns = append(tables[idx].Columns, col)
		}
	}
	return rows.Err()
}

func queryIndexes(ctx context.Context, db *sql.DB, schema string, tables []TableInfo, tableIdx map[string]int) error {
	rows, err := db.QueryContext(ctx, `
SELECT
  TABLE_NAME,
  INDEX_NAME,
  COLUMN_NAME,
  NON_UNIQUE,
  INDEX_TYPE
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = ?
ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, schema)
	if err != nil {
		return fmt.Errorf("querying STATISTICS for %s: %w", schema, err)
	}
	defer rows.Close()

	// Accumulate columns per (table, index).
	type key struct{ table, index string }
	type entry struct {
		idx       int   // position in tables[tableIdx[table]].Indexes
		nonUnique bool
		indexType string
	}
	perTable := make(map[string]map[string]*entry) // table → index → entry

	for rows.Next() {
		var tableName, indexName, colName, indexType string
		var nonUnique int
		if err := rows.Scan(&tableName, &indexName, &colName, &nonUnique, &indexType); err != nil {
			continue
		}
		tIdx, ok := tableIdx[tableName]
		if !ok {
			continue
		}
		if perTable[tableName] == nil {
			perTable[tableName] = make(map[string]*entry)
		}
		e, exists := perTable[tableName][indexName]
		if !exists {
			info := IndexInfo{
				Name:      indexName,
				NonUnique: nonUnique != 0,
				IndexType: indexType,
			}
			tables[tIdx].Indexes = append(tables[tIdx].Indexes, info)
			e = &entry{
				idx:       len(tables[tIdx].Indexes) - 1,
				nonUnique: nonUnique != 0,
				indexType: indexType,
			}
			perTable[tableName][indexName] = e
		}
		tables[tIdx].Indexes[e.idx].Columns = append(tables[tIdx].Indexes[e.idx].Columns, colName)
	}
	return rows.Err()
}

func queryConstraints(ctx context.Context, db *sql.DB, schema string, tables []TableInfo, tableIdx map[string]int) error {
	rows, err := db.QueryContext(ctx, `
SELECT
  tc.TABLE_NAME,
  tc.CONSTRAINT_NAME,
  tc.CONSTRAINT_TYPE,
  COALESCE(kcu.COLUMN_NAME, ''),
  COALESCE(kcu.REFERENCED_TABLE_NAME, '')
FROM information_schema.TABLE_CONSTRAINTS tc
LEFT JOIN information_schema.KEY_COLUMN_USAGE kcu
  ON kcu.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
  AND kcu.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
  AND kcu.TABLE_NAME = tc.TABLE_NAME
WHERE tc.TABLE_SCHEMA = ?
ORDER BY tc.TABLE_NAME, tc.CONSTRAINT_NAME, kcu.ORDINAL_POSITION`, schema)
	if err != nil {
		return fmt.Errorf("querying TABLE_CONSTRAINTS for %s: %w", schema, err)
	}
	defer rows.Close()

	type key struct{ table, constraint string }
	type entry struct{ idx int }
	seen := make(map[key]*entry)

	for rows.Next() {
		var tableName, constraintName, constraintType, colName, refTable string
		if err := rows.Scan(&tableName, &constraintName, &constraintType, &colName, &refTable); err != nil {
			continue
		}
		tIdx, ok := tableIdx[tableName]
		if !ok {
			continue
		}
		k := key{tableName, constraintName}
		e, exists := seen[k]
		if !exists {
			con := ConstraintInfo{
				Name:            constraintName,
				Type:            constraintType,
				ReferencedTable: refTable,
			}
			tables[tIdx].Constraints = append(tables[tIdx].Constraints, con)
			e = &entry{idx: len(tables[tIdx].Constraints) - 1}
			seen[k] = e
		}
		if colName != "" {
			tables[tIdx].Constraints[e.idx].Columns = append(
				tables[tIdx].Constraints[e.idx].Columns, colName)
		}
		if refTable != "" && tables[tIdx].Constraints[e.idx].ReferencedTable == "" {
			tables[tIdx].Constraints[e.idx].ReferencedTable = refTable
		}
	}
	return rows.Err()
}

func queryRoutines(ctx context.Context, db *sql.DB, schema string) ([]RoutineInfo, error) {
	rows, err := db.QueryContext(ctx, `
SELECT
  ROUTINE_NAME,
  ROUTINE_TYPE,
  COALESCE(DEFINER, ''),
  COALESCE(SQL_MODE, ''),
  IF(IS_DETERMINISTIC = 'YES', 1, 0),
  COALESCE(SECURITY_TYPE, ''),
  COALESCE(DATE_FORMAT(CREATED, '%Y-%m-%d %H:%i:%s'), ''),
  COALESCE(DATE_FORMAT(LAST_ALTERED, '%Y-%m-%d %H:%i:%s'), '')
FROM information_schema.ROUTINES
WHERE ROUTINE_SCHEMA = ?
ORDER BY ROUTINE_TYPE, ROUTINE_NAME`, schema)
	if err != nil {
		return nil, fmt.Errorf("querying ROUTINES for %s: %w", schema, err)
	}
	defer rows.Close()

	var routines []RoutineInfo
	for rows.Next() {
		var r RoutineInfo
		var deterministic int
		if err := rows.Scan(
			&r.Name, &r.Type, &r.Definer, &r.SQLMode,
			&deterministic, &r.SecurityType, &r.Created, &r.Modified,
		); err != nil {
			continue
		}
		r.IsDeterministic = deterministic == 1
		routines = append(routines, r)
	}
	return routines, rows.Err()
}

func queryTriggers(ctx context.Context, db *sql.DB, schema string) ([]TriggerInfo, error) {
	rows, err := db.QueryContext(ctx, `
SELECT
  TRIGGER_NAME,
  EVENT_MANIPULATION,
  ACTION_TIMING,
  EVENT_OBJECT_TABLE,
  COALESCE(DEFINER, ''),
  COALESCE(SQL_MODE, ''),
  COALESCE(DATE_FORMAT(CREATED, '%Y-%m-%d %H:%i:%s'), '')
FROM information_schema.TRIGGERS
WHERE TRIGGER_SCHEMA = ?
ORDER BY TRIGGER_NAME`, schema)
	if err != nil {
		return nil, fmt.Errorf("querying TRIGGERS for %s: %w", schema, err)
	}
	defer rows.Close()

	var triggers []TriggerInfo
	for rows.Next() {
		var tr TriggerInfo
		if err := rows.Scan(
			&tr.Name, &tr.Event, &tr.Timing, &tr.Table,
			&tr.Definer, &tr.SQLMode, &tr.Created,
		); err != nil {
			continue
		}
		triggers = append(triggers, tr)
	}
	return triggers, rows.Err()
}

func queryEvents(ctx context.Context, db *sql.DB, schema string) ([]EventInfo, error) {
	rows, err := db.QueryContext(ctx, `
SELECT
  EVENT_NAME,
  COALESCE(DEFINER, ''),
  COALESCE(STATUS, ''),
  COALESCE(DATE_FORMAT(EXECUTE_AT, '%Y-%m-%d %H:%i:%s'), ''),
  COALESCE(INTERVAL_VALUE, ''),
  COALESCE(INTERVAL_FIELD, ''),
  COALESCE(DATE_FORMAT(STARTS, '%Y-%m-%d %H:%i:%s'), ''),
  COALESCE(DATE_FORMAT(ENDS, '%Y-%m-%d %H:%i:%s'), '')
FROM information_schema.EVENTS
WHERE EVENT_SCHEMA = ?
ORDER BY EVENT_NAME`, schema)
	if err != nil {
		return nil, fmt.Errorf("querying EVENTS for %s: %w", schema, err)
	}
	defer rows.Close()

	var events []EventInfo
	for rows.Next() {
		var e EventInfo
		if err := rows.Scan(
			&e.Name, &e.Definer, &e.Status,
			&e.ExecuteAt, &e.IntervalValue, &e.IntervalField,
			&e.Starts, &e.Ends,
		); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// serverVersion queries the MySQL server version string.
func serverVersion(ctx context.Context, db *sql.DB) (string, error) {
	var version string
	err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version)
	if err != nil {
		return "", fmt.Errorf("querying VERSION(): %w", err)
	}
	return version, nil
}

// isReadReplica checks whether the server is a read replica.
func isReadReplica(ctx context.Context, db *sql.DB) bool {
	var readOnly string
	err := db.QueryRowContext(ctx, "SELECT @@global.read_only").Scan(&readOnly)
	if err != nil {
		return false
	}
	return readOnly == "1" || strings.ToLower(readOnly) == "on"
}
