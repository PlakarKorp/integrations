package manifest

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-postgresql/pgconn"
)

// fieldSep is the column delimiter in psql unaligned output.  SOH (0x01)
// never appears in PostgreSQL identifiers, settings, or SQL text.
const fieldSep = "\x01"

// arraySep delimits elements within a single field that holds a PostgreSQL
// array value (reloptions, spcoptions, constraint column lists, …).
// STX (0x02) is equally safe for the same reason.
const arraySep = "\x02"

// --- Types ---

// ClusterConfig holds key server configuration parameters.
type ClusterConfig struct {
	DataDirectory          string `json:"data_directory,omitempty"`
	Timezone               string `json:"timezone,omitempty"`
	MaxConnections         int    `json:"max_connections,omitempty"`
	WalLevel               string `json:"wal_level,omitempty"`
	ServerEncoding         string `json:"server_encoding,omitempty"`
	DataChecksums          bool   `json:"data_checksums"`
	BlockSize              int    `json:"block_size,omitempty"`
	WalBlockSize           int    `json:"wal_block_size,omitempty"`
	SharedPreloadLibraries string `json:"shared_preload_libraries,omitempty"`
	LCCollate              string `json:"lc_collate,omitempty"`
	LCCType                string `json:"lc_ctype,omitempty"`
	ArchiveMode            string `json:"archive_mode,omitempty"`
	ArchiveCommandSet      bool   `json:"archive_command_set"` // true when archive_command is non-empty; the command itself is not stored to avoid leaking credentials
}

// Role describes a PostgreSQL role and its group memberships.
type Role struct {
	Name            string     `json:"name"`
	Superuser       bool       `json:"superuser"`
	Replication     bool       `json:"replication"`
	CanLogin        bool       `json:"can_login"`
	CreateDB        bool       `json:"create_db"`
	CreateRole      bool       `json:"create_role"`
	Inherit         bool       `json:"inherit"`
	BypassRLS       bool       `json:"bypass_rls"`
	ConnectionLimit int        `json:"connection_limit"` // -1 = no limit
	ValidUntil      *time.Time `json:"valid_until"`
	MemberOf        []string   `json:"member_of,omitempty"`
}

// Tablespace describes a PostgreSQL tablespace.
type Tablespace struct {
	Name     string   `json:"name"`
	Owner    string   `json:"owner"`
	Location string   `json:"location"`
	Options  []string `json:"options,omitempty"` // e.g. seq_page_cost=1.1
}

// Extension describes an installed PostgreSQL extension.
type Extension struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Schema  string `json:"schema"`
}

// SchemaInfo describes one schema within a database.
type SchemaInfo struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// ColumnInfo describes one column of a relation.
type ColumnInfo struct {
	Position int    `json:"position"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default,omitempty"`
}

// ConstraintInfo describes one constraint on a relation.
// Type codes: p=primary key, u=unique, f=foreign key, c=check, x=exclusion.
// Columns lists the constrained column names (empty for check constraints on
// expressions and for exclusion constraints).
type ConstraintInfo struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Columns []string `json:"columns,omitempty"`
}

// IndexInfo describes one index on a relation.
type IndexInfo struct {
	Name           string `json:"name"`
	Method         string `json:"method"` // btree, hash, gist, gin, brin, …
	Definition     string `json:"definition"`
	IsUnique       bool   `json:"is_unique"`
	IsPrimary      bool   `json:"is_primary"`
	IsValid        bool   `json:"is_valid"`
	IsPartial      bool   `json:"is_partial"` // has a WHERE predicate
	ConstraintName string `json:"constraint_name,omitempty"`
}

// RelationInfo describes any pg_class relation: ordinary table, partitioned
// table, foreign table, view, materialized view, or sequence.
// The Kind field uses single-letter relkind codes: r=table, p=partitioned
// table, f=foreign table, v=view, m=materialized view, S=sequence.
type RelationInfo struct {
	Schema            string           `json:"schema"`
	Name              string           `json:"name"`
	Owner             string           `json:"owner"`
	Persistence       string           `json:"persistence"`          // p=permanent, u=unlogged, t=temp
	Kind              string           `json:"kind"`                  // see above
	Tablespace        string           `json:"tablespace,omitempty"`
	RowEstimate       int64            `json:"row_estimate"`      // reltuples (fast, may be stale)
	LiveRowEstimate   int64            `json:"live_row_estimate"` // n_live_tup from autovacuum
	ColumnCount       int              `json:"column_count"`
	HasPrimaryKey     bool             `json:"has_primary_key"`
	HasTriggers       bool             `json:"has_triggers"`
	RLSEnabled        bool             `json:"rls_enabled"`
	RLSForced         bool             `json:"rls_forced"`
	IsPartition       bool             `json:"is_partition"`
	PartitionParent   string           `json:"partition_parent,omitempty"`   // "schema.name"
	PartitionStrategy string           `json:"partition_strategy,omitempty"` // range, list, hash
	StorageOptions    []string         `json:"storage_options,omitempty"`    // reloptions
	LastVacuum        *time.Time       `json:"last_vacuum"`
	LastAnalyze       *time.Time       `json:"last_analyze"`
	Columns           []ColumnInfo     `json:"columns,omitempty"`
	Constraints       []ConstraintInfo `json:"constraints,omitempty"`
	Indexes           []IndexInfo      `json:"indexes,omitempty"`
}

// DatabaseInfo describes a PostgreSQL database and its contents.
type DatabaseInfo struct {
	Name              string         `json:"name"`
	Owner             string         `json:"owner"`
	Encoding          string         `json:"encoding"`
	Collate           string         `json:"collate"`
	CType             string         `json:"ctype"`
	AllowConn         bool           `json:"allow_conn"`
	IsTemplate        bool           `json:"is_template"`
	DefaultTablespace string         `json:"default_tablespace,omitempty"`
	ConnectionLimit   int            `json:"connection_limit"` // -1 = no limit
	Extensions        []Extension    `json:"extensions,omitempty"`
	Schemas           []SchemaInfo   `json:"schemas,omitempty"`
	Relations         []RelationInfo `json:"relations,omitempty"`
}

// --- Helpers ---

func queryRows(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, dbname, query string) ([][]string, error) {
	args := append(conn.Args(), "-d", dbname, "-t", "-A", "-F", fieldSep, "--no-psqlrc", "-c", query)
	cmd := exec.CommandContext(ctx, psqlBin, args...)
	cmd.Stdin = nil
	cmd.Env = conn.Env()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, fmt.Errorf("%w: %s", err, s)
		}
		return nil, err
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, fieldSep))
	}
	return rows, nil
}

func parseBool(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "t" || s == "true" || s == "1" || s == "on" || s == "yes"
}

func parseInt(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

// parseEpoch converts a Unix-epoch string (EXTRACT(EPOCH …)::bigint) into a
// *time.Time.  Returns nil for empty, "-1", or non-positive values.
func parseEpoch(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" || s == "-1" {
		return nil
	}
	sec := parseInt64(s)
	if sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

// splitArray splits a chr(2)-delimited string into a slice.
// Returns nil for empty input so JSON omitempty works correctly.
func splitArray(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, arraySep)
}

// collectClusterMetadata populates the cluster-level fields of m
// (ClusterSystemIdentifier, InRecovery, ClusterConfig, Roles, Tablespaces,
// and a basic Databases list) by connecting to connectDB.  Individual query
// failures are silently ignored so that partial metadata never aborts a
// backup.
func collectClusterMetadata(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, connectDB string, m *Manifest) {
	m.ClusterSystemIdentifier = queryClusterSystemID(ctx, psqlBin, conn, connectDB)
	m.InRecovery = queryInRecovery(ctx, psqlBin, conn, connectDB)
	if cfg, err := queryClusterConfig(ctx, psqlBin, conn, connectDB); err == nil {
		m.ClusterConfig = &cfg
	}
	if roles, err := queryRoles(ctx, psqlBin, conn, connectDB); err == nil {
		m.Roles = roles
	}
	if tss, err := queryTablespaces(ctx, psqlBin, conn, connectDB); err == nil {
		m.Tablespaces = tss
	}
	if dbs, err := queryDatabases(ctx, psqlBin, conn, connectDB); err == nil {
		m.Databases = dbs
	}
}

// pgDumpVersion returns the version string reported by pg_dump --version,
// e.g. "pg_dump (PostgreSQL) 16.2".  Returns an empty string on error.
func pgDumpVersion(pgDumpBin string) string {
	out, err := exec.Command(pgDumpBin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// pgBaseBackupVersion returns the version string reported by
// pg_basebackup --version, e.g. "pg_basebackup (PostgreSQL) 16.2".
// Returns an empty string on error.
func pgBaseBackupVersion(pgBaseBackupBin string) string {
	out, err := exec.Command(pgBaseBackupBin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// QueryClusterSystemID returns the PostgreSQL cluster system identifier from
// pg_control_system().  Returns an empty string when not accessible (e.g.
// the backup user lacks pg_monitor or superuser).
func queryClusterSystemID(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, connectDB string) string {
	rows, err := queryRows(ctx, psqlBin, conn, connectDB,
		`SELECT system_identifier::text FROM pg_control_system()`)
	if err != nil || len(rows) == 0 || len(rows[0]) == 0 {
		return ""
	}
	return rows[0][0]
}

// QueryInRecovery reports whether the server is currently a hot-standby
// (i.e. the backup was taken from a replica rather than the primary).
func queryInRecovery(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, connectDB string) bool {
	rows, err := queryRows(ctx, psqlBin, conn, connectDB,
		`SELECT pg_is_in_recovery()`)
	if err != nil || len(rows) == 0 || len(rows[0]) == 0 {
		return false
	}
	return parseBool(rows[0][0])
}

// QueryClusterConfig queries key server GUCs.  connectDB is any database the
// caller already has access to; GUC data is cluster-wide regardless of the
// connection database.
func queryClusterConfig(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, connectDB string) (ClusterConfig, error) {
	rows, err := queryRows(ctx, psqlBin, conn, connectDB,
		`SELECT name, setting FROM pg_settings `+
			`WHERE name IN (`+
			`'archive_command','archive_mode','block_size','data_checksums',`+
			`'data_directory','lc_collate','lc_ctype','max_connections',`+
			`'server_encoding','shared_preload_libraries','timezone',`+
			`'wal_block_size','wal_level') `+
			`ORDER BY name`)
	if err != nil {
		return ClusterConfig{}, fmt.Errorf("query cluster config: %w", err)
	}
	var cfg ClusterConfig
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		switch r[0] {
		case "archive_command":
			cfg.ArchiveCommandSet = r[1] != "" && r[1] != "(disabled)"
		case "archive_mode":
			cfg.ArchiveMode = r[1]
		case "block_size":
			cfg.BlockSize = parseInt(r[1])
		case "data_checksums":
			cfg.DataChecksums = parseBool(r[1])
		case "data_directory":
			cfg.DataDirectory = r[1]
		case "lc_collate":
			cfg.LCCollate = r[1]
		case "lc_ctype":
			cfg.LCCType = r[1]
		case "max_connections":
			cfg.MaxConnections = parseInt(r[1])
		case "server_encoding":
			cfg.ServerEncoding = r[1]
		case "shared_preload_libraries":
			cfg.SharedPreloadLibraries = r[1]
		case "timezone":
			cfg.Timezone = r[1]
		case "wal_block_size":
			cfg.WalBlockSize = parseInt(r[1])
		case "wal_level":
			cfg.WalLevel = r[1]
		}
	}
	return cfg, nil
}

// QueryRoles queries all PostgreSQL roles with their attributes and group
// memberships.
func queryRoles(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, connectDB string) ([]Role, error) {
	rows, err := queryRows(ctx, psqlBin, conn, connectDB,
		`SELECT rolname, rolsuper, rolreplication, rolcanlogin, rolcreatedb, `+
			`rolcreaterole, rolinherit, rolbypassrls, rolconnlimit, `+
			`CASE WHEN rolvaliduntil IS NULL OR rolvaliduntil = 'infinity'::timestamptz `+
			`THEN -1 ELSE EXTRACT(EPOCH FROM rolvaliduntil)::bigint END `+
			`FROM pg_roles ORDER BY rolname`)
	if err != nil {
		return nil, fmt.Errorf("query roles: %w", err)
	}

	roles := make([]Role, 0, len(rows))
	roleIdx := make(map[string]int)
	for _, r := range rows {
		if len(r) < 10 {
			continue
		}
		role := Role{
			Name:            r[0],
			Superuser:       parseBool(r[1]),
			Replication:     parseBool(r[2]),
			CanLogin:        parseBool(r[3]),
			CreateDB:        parseBool(r[4]),
			CreateRole:      parseBool(r[5]),
			Inherit:         parseBool(r[6]),
			BypassRLS:       parseBool(r[7]),
			ConnectionLimit: parseInt(r[8]),
			ValidUntil:      parseEpoch(r[9]),
		}
		roleIdx[role.Name] = len(roles)
		roles = append(roles, role)
	}

	// Attach memberships: for each role, list the roles it belongs to.
	memRows, err := queryRows(ctx, psqlBin, conn, connectDB,
		`SELECT m.rolname, r.rolname `+
			`FROM pg_auth_members am `+
			`JOIN pg_roles r ON r.oid = am.roleid `+
			`JOIN pg_roles m ON m.oid = am.member `+
			`ORDER BY m.rolname, r.rolname`)
	if err == nil {
		for _, r := range memRows {
			if len(r) < 2 {
				continue
			}
			if idx, ok := roleIdx[r[0]]; ok {
				roles[idx].MemberOf = append(roles[idx].MemberOf, r[1])
			}
		}
	}

	return roles, nil
}

// QueryTablespaces queries all tablespaces with their owners, locations,
// sizes, and storage options.
func queryTablespaces(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, connectDB string) ([]Tablespace, error) {
	rows, err := queryRows(ctx, psqlBin, conn, connectDB,
		`SELECT spcname, pg_get_userbyid(spcowner), `+
			`pg_tablespace_location(oid), `+
			`COALESCE(array_to_string(spcoptions, chr(2)), '') `+
			`FROM pg_tablespace ORDER BY spcname`)
	if err != nil {
		return nil, fmt.Errorf("query tablespaces: %w", err)
	}
	var tss []Tablespace
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		tss = append(tss, Tablespace{
			Name:     r[0],
			Owner:    r[1],
			Location: r[2],
			Options:  splitArray(r[3]),
		})
	}
	return tss, nil
}

// QueryDatabases queries the list of all databases with basic metadata.
// Per-database detail (schemas, extensions, relations) is not fetched here;
// call QueryDatabaseDetail for each database that needs it.
func queryDatabases(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, connectDB string) ([]DatabaseInfo, error) {
	rows, err := queryRows(ctx, psqlBin, conn, connectDB,
		`SELECT d.datname, pg_get_userbyid(d.datdba), pg_encoding_to_char(d.encoding), `+
			`d.datcollate, d.datctype, d.datallowconn, d.datistemplate, `+
			`d.datconnlimit, `+
			`COALESCE(ts.spcname, 'pg_default') `+
			`FROM pg_database d `+
			`LEFT JOIN pg_tablespace ts ON ts.oid = d.dattablespace `+
			`ORDER BY d.datname`)
	if err != nil {
		return nil, fmt.Errorf("query databases: %w", err)
	}
	var dbs []DatabaseInfo
	for _, r := range rows {
		if len(r) < 9 {
			continue
		}
		dbs = append(dbs, DatabaseInfo{
			Name:              r[0],
			Owner:             r[1],
			Encoding:          r[2],
			Collate:           r[3],
			CType:             r[4],
			AllowConn:         parseBool(r[5]),
			IsTemplate:        parseBool(r[6]),
			ConnectionLimit:   parseInt(r[7]),
			DefaultTablespace: r[8],
		})
	}
	return dbs, nil
}

// QueryDatabaseDetail populates db.Extensions, db.Schemas, and db.Relations
// (including columns, constraints, and indexes) by connecting to db.Name.
func queryDatabaseDetail(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, db *DatabaseInfo) error {
	if err := queryExtensions(ctx, psqlBin, conn, db); err != nil {
		return err
	}
	if err := querySchemas(ctx, psqlBin, conn, db); err != nil {
		return err
	}
	return queryRelations(ctx, psqlBin, conn, db)
}

func queryExtensions(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, db *DatabaseInfo) error {
	rows, err := queryRows(ctx, psqlBin, conn, db.Name,
		`SELECT e.extname, e.extversion, n.nspname `+
			`FROM pg_extension e `+
			`JOIN pg_namespace n ON n.oid = e.extnamespace `+
			`ORDER BY e.extname`)
	if err != nil {
		return fmt.Errorf("query extensions for %s: %w", db.Name, err)
	}
	for _, r := range rows {
		if len(r) < 3 {
			continue
		}
		db.Extensions = append(db.Extensions, Extension{Name: r[0], Version: r[1], Schema: r[2]})
	}
	return nil
}

func querySchemas(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, db *DatabaseInfo) error {
	rows, err := queryRows(ctx, psqlBin, conn, db.Name,
		`SELECT nspname, pg_get_userbyid(nspowner) `+
			`FROM pg_namespace `+
			`WHERE nspname NOT IN ('pg_catalog', 'information_schema') `+
			`AND nspname NOT LIKE 'pg_toast%' `+
			`AND nspname NOT LIKE 'pg_temp%' `+
			`ORDER BY nspname`)
	if err != nil {
		return fmt.Errorf("query schemas for %s: %w", db.Name, err)
	}
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		db.Schemas = append(db.Schemas, SchemaInfo{Name: r[0], Owner: r[1]})
	}
	return nil
}

func queryRelations(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, db *DatabaseInfo) error {
	// Relations: ordinary tables, partitioned tables, foreign tables, views,
	// materialized views, and sequences.
	relRows, err := queryRows(ctx, psqlBin, conn, db.Name, `
SELECT
  n.nspname,
  c.relname,
  pg_get_userbyid(c.relowner),
  c.relpersistence,
  c.relkind,
  GREATEST(c.reltuples::bigint, 0),
  COALESCE(s.n_live_tup, 0),
  EXISTS(SELECT 1 FROM pg_constraint k WHERE k.conrelid = c.oid AND k.contype = 'p'),
  c.relhastriggers,
  c.relrowsecurity,
  c.relforcerowsecurity,
  c.relispartition,
  COALESCE(array_to_string(c.reloptions, chr(2)), ''),
  COALESCE((
    SELECT pn.nspname || '.' || p.relname
    FROM pg_inherits i
    JOIN pg_class p ON p.oid = i.inhparent
    JOIN pg_namespace pn ON pn.oid = p.relnamespace
    WHERE i.inhrelid = c.oid LIMIT 1
  ), ''),
  COALESCE((
    SELECT CASE pt.partstrat
      WHEN 'r' THEN 'range'
      WHEN 'l' THEN 'list'
      WHEN 'h' THEN 'hash'
    END
    FROM pg_partitioned_table pt WHERE pt.partrelid = c.oid
  ), ''),
  COALESCE(ts.spcname, ''),
  COALESCE(EXTRACT(EPOCH FROM s.last_vacuum)::bigint, -1),
  COALESCE(EXTRACT(EPOCH FROM s.last_analyze)::bigint, -1)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
LEFT JOIN pg_tablespace ts ON ts.oid = c.reltablespace
WHERE c.relkind IN ('r','p','f','m','v','S')
  AND n.nspname NOT IN ('pg_catalog','information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT LIKE 'pg_temp%'
ORDER BY n.nspname, c.relname`)
	if err != nil {
		return fmt.Errorf("query relations for %s: %w", db.Name, err)
	}

	relIdx := make(map[string]int) // "schema.name" → position in db.Relations
	for _, r := range relRows {
		if len(r) < 18 {
			continue
		}
		rel := RelationInfo{
			Schema:            r[0],
			Name:              r[1],
			Owner:             r[2],
			Persistence:       r[3],
			Kind:              r[4],
			RowEstimate:       parseInt64(r[5]),
			LiveRowEstimate:   parseInt64(r[6]),
			HasPrimaryKey:     parseBool(r[7]),
			HasTriggers:       parseBool(r[8]),
			RLSEnabled:        parseBool(r[9]),
			RLSForced:         parseBool(r[10]),
			IsPartition:       parseBool(r[11]),
			StorageOptions:    splitArray(r[12]),
			PartitionParent:   r[13],
			PartitionStrategy: r[14],
			Tablespace:        r[15],
			LastVacuum:        parseEpoch(r[16]),
			LastAnalyze:       parseEpoch(r[17]),
		}
		relIdx[r[0]+"."+r[1]] = len(db.Relations)
		db.Relations = append(db.Relations, rel)
	}

	// Columns.
	colRows, err := queryRows(ctx, psqlBin, conn, db.Name, `
SELECT
  n.nspname,
  c.relname,
  a.attnum,
  a.attname,
  pg_catalog.format_type(a.atttypid, a.atttypmod),
  NOT a.attnotnull,
  COALESCE(pg_get_expr(d.adbin, d.adrelid), '')
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attnum > 0
  AND NOT a.attisdropped
  AND c.relkind IN ('r','p','f','m','v','S')
  AND n.nspname NOT IN ('pg_catalog','information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT LIKE 'pg_temp%'
ORDER BY n.nspname, c.relname, a.attnum`)
	if err != nil {
		return fmt.Errorf("query columns for %s: %w", db.Name, err)
	}
	for _, r := range colRows {
		if len(r) < 7 {
			continue
		}
		idx, ok := relIdx[r[0]+"."+r[1]]
		if !ok {
			continue
		}
		db.Relations[idx].Columns = append(db.Relations[idx].Columns, ColumnInfo{
			Position: parseInt(r[2]),
			Name:     r[3],
			Type:     r[4],
			Nullable: parseBool(r[5]),
			Default:  r[6],
		})
	}
	// Set ColumnCount from actual queried columns (more accurate than relnatts).
	for i := range db.Relations {
		db.Relations[i].ColumnCount = len(db.Relations[i].Columns)
	}

	// Constraints (primary key, unique, foreign key, check, exclusion).
	// We store the constrained column names; full expressions are in the dump.
	conRows, err := queryRows(ctx, psqlBin, conn, db.Name, `
SELECT
  n.nspname,
  c.relname,
  con.conname,
  con.contype::text,
  COALESCE(array_to_string(ARRAY(
    SELECT a.attname
    FROM pg_attribute a
    WHERE a.attrelid = c.oid
      AND a.attnum = ANY(COALESCE(con.conkey, '{}'))
    ORDER BY a.attnum
  ), chr(2)), '')
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname NOT IN ('pg_catalog','information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT LIKE 'pg_temp%'
  AND con.contype IN ('p','u','f','c','x')
ORDER BY n.nspname, c.relname, con.conname`)
	if err != nil {
		return fmt.Errorf("query constraints for %s: %w", db.Name, err)
	}
	for _, r := range conRows {
		if len(r) < 5 {
			continue
		}
		idx, ok := relIdx[r[0]+"."+r[1]]
		if !ok {
			continue
		}
		db.Relations[idx].Constraints = append(db.Relations[idx].Constraints, ConstraintInfo{
			Name:    r[2],
			Type:    r[3],
			Columns: splitArray(r[4]),
		})
	}

	// Indexes with structured metadata.
	idxRows, err := queryRows(ctx, psqlBin, conn, db.Name, `
SELECT
  ix.schemaname,
  ix.tablename,
  ix.indexname,
  am.amname,
  replace(ix.indexdef, E'\n', ' '),
  i.indisunique,
  i.indisprimary,
  i.indisvalid,
  i.indpred IS NOT NULL,
  COALESCE((SELECT con.conname FROM pg_constraint con WHERE con.conindid = c.oid LIMIT 1), '')
FROM pg_indexes ix
JOIN pg_class c ON c.relname = ix.indexname
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = ix.schemaname
JOIN pg_index i ON i.indexrelid = c.oid
JOIN pg_am am ON am.oid = c.relam
WHERE ix.schemaname NOT IN ('pg_catalog','information_schema')
  AND ix.schemaname NOT LIKE 'pg_temp%'
ORDER BY ix.schemaname, ix.tablename, ix.indexname`)
	if err != nil {
		return fmt.Errorf("query indexes for %s: %w", db.Name, err)
	}
	for _, r := range idxRows {
		if len(r) < 10 {
			continue
		}
		idx, ok := relIdx[r[0]+"."+r[1]]
		if !ok {
			continue
		}
		db.Relations[idx].Indexes = append(db.Relations[idx].Indexes, IndexInfo{
			Name:           r[2],
			Method:         r[3],
			Definition:     r[4],
			IsUnique:       parseBool(r[5]),
			IsPrimary:      parseBool(r[6]),
			IsValid:        parseBool(r[7]),
			IsPartial:      parseBool(r[8]),
			ConstraintName: r[9],
		})
	}

	return nil
}
