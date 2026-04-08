package manifest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-postgresql/pgconn"
)

// arraySep delimits elements within a single field that holds a PostgreSQL
// array value (reloptions, spcoptions, constraint column lists, …).
// STX (0x02) is equally safe for identifiers, settings, or SQL text.
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

// splitArray splits a chr(2)-delimited string into a slice.
// Returns nil for empty input so JSON omitempty works correctly.
func splitArray(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, arraySep)
}

// epochToTime converts a Unix-epoch int64 into a *time.Time.
// Returns nil for -1 or non-positive values (used for COALESCE(…, -1) results).
func epochToTime(sec int64) *time.Time {
	if sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

// collectClusterMetadata populates the cluster-level fields of m
// (ClusterSystemIdentifier, InRecovery, ClusterConfig, Roles, Tablespaces,
// and a basic Databases list) by using db.  Individual query failures are
// silently ignored so that partial metadata never aborts a backup.
func collectClusterMetadata(ctx context.Context, db *sql.DB, m *Manifest) {
	m.ClusterSystemIdentifier = queryClusterSystemID(ctx, db)
	m.InRecovery = queryInRecovery(ctx, db)
	if cfg, err := queryClusterConfig(ctx, db); err == nil {
		m.ClusterConfig = &cfg
	}
	if roles, err := queryRoles(ctx, db); err == nil {
		m.Roles = roles
	}
	if tss, err := queryTablespaces(ctx, db); err == nil {
		m.Tablespaces = tss
	}
	if dbs, err := queryDatabases(ctx, db); err == nil {
		m.Databases = dbs
	}
}

// queryDatabaseDetail populates db.Extensions, db.Schemas, and db.Relations
// (including columns, constraints, and indexes) by opening a new connection
// to db.Name.
func queryDatabaseDetail(ctx context.Context, conn pgconn.ConnConfig, db *DatabaseInfo) error {
	sqlDB, err := conn.Open(db.Name)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", db.Name, err)
	}
	defer sqlDB.Close()

	if err := queryExtensions(ctx, sqlDB, db); err != nil {
		return err
	}
	if err := querySchemas(ctx, sqlDB, db); err != nil {
		return err
	}
	return queryRelations(ctx, sqlDB, db)
}

func queryClusterSystemID(ctx context.Context, db *sql.DB) string {
	var id string
	_ = db.QueryRowContext(ctx,
		`SELECT system_identifier::text FROM pg_control_system()`).Scan(&id)
	return id
}

func queryInRecovery(ctx context.Context, db *sql.DB) bool {
	var inRecovery bool
	_ = db.QueryRowContext(ctx, `SELECT pg_is_in_recovery()`).Scan(&inRecovery)
	return inRecovery
}

func queryClusterConfig(ctx context.Context, db *sql.DB) (ClusterConfig, error) {
	rows, err := db.QueryContext(ctx,
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
	defer rows.Close()

	var cfg ClusterConfig
	for rows.Next() {
		var name, setting string
		if err := rows.Scan(&name, &setting); err != nil {
			continue
		}
		switch name {
		case "archive_command":
			cfg.ArchiveCommandSet = setting != "" && setting != "(disabled)"
		case "archive_mode":
			cfg.ArchiveMode = setting
		case "block_size":
			fmt.Sscanf(setting, "%d", &cfg.BlockSize)
		case "data_checksums":
			cfg.DataChecksums = setting == "on"
		case "data_directory":
			cfg.DataDirectory = setting
		case "lc_collate":
			cfg.LCCollate = setting
		case "lc_ctype":
			cfg.LCCType = setting
		case "max_connections":
			fmt.Sscanf(setting, "%d", &cfg.MaxConnections)
		case "server_encoding":
			cfg.ServerEncoding = setting
		case "shared_preload_libraries":
			cfg.SharedPreloadLibraries = setting
		case "timezone":
			cfg.Timezone = setting
		case "wal_block_size":
			fmt.Sscanf(setting, "%d", &cfg.WalBlockSize)
		case "wal_level":
			cfg.WalLevel = setting
		}
	}
	return cfg, rows.Err()
}

func queryRoles(ctx context.Context, db *sql.DB) ([]Role, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT rolname, rolsuper, rolreplication, rolcanlogin, rolcreatedb, `+
			`rolcreaterole, rolinherit, rolbypassrls, rolconnlimit, `+
			`CASE WHEN rolvaliduntil = 'infinity'::timestamptz THEN NULL `+
			`ELSE rolvaliduntil END `+
			`FROM pg_roles ORDER BY rolname`)
	if err != nil {
		return nil, fmt.Errorf("query roles: %w", err)
	}
	defer rows.Close()

	var roles []Role
	roleIdx := make(map[string]int)
	for rows.Next() {
		var (
			name                                                   string
			superuser, replication, canLogin, createDB, createRole bool
			inherit, bypassRLS                                     bool
			connLimit                                              int
			validUntil                                             sql.NullTime
		)
		if err := rows.Scan(&name, &superuser, &replication, &canLogin, &createDB,
			&createRole, &inherit, &bypassRLS, &connLimit, &validUntil); err != nil {
			continue
		}
		role := Role{
			Name:            name,
			Superuser:       superuser,
			Replication:     replication,
			CanLogin:        canLogin,
			CreateDB:        createDB,
			CreateRole:      createRole,
			Inherit:         inherit,
			BypassRLS:       bypassRLS,
			ConnectionLimit: connLimit,
		}
		if validUntil.Valid {
			t := validUntil.Time.UTC()
			role.ValidUntil = &t
		}
		roleIdx[role.Name] = len(roles)
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query roles: %w", err)
	}

	// Attach memberships: for each role, list the roles it belongs to.
	memRows, err := db.QueryContext(ctx,
		`SELECT m.rolname, r.rolname `+
			`FROM pg_auth_members am `+
			`JOIN pg_roles r ON r.oid = am.roleid `+
			`JOIN pg_roles m ON m.oid = am.member `+
			`ORDER BY m.rolname, r.rolname`)
	if err == nil {
		defer memRows.Close()
		for memRows.Next() {
			var member, parent string
			if err := memRows.Scan(&member, &parent); err != nil {
				continue
			}
			if idx, ok := roleIdx[member]; ok {
				roles[idx].MemberOf = append(roles[idx].MemberOf, parent)
			}
		}
	}

	return roles, nil
}

func queryTablespaces(ctx context.Context, db *sql.DB) ([]Tablespace, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT spcname, pg_get_userbyid(spcowner), `+
			`pg_tablespace_location(oid), `+
			`COALESCE(array_to_string(spcoptions, chr(2)), '') `+
			`FROM pg_tablespace ORDER BY spcname`)
	if err != nil {
		return nil, fmt.Errorf("query tablespaces: %w", err)
	}
	defer rows.Close()

	var tss []Tablespace
	for rows.Next() {
		var name, owner, location, options string
		if err := rows.Scan(&name, &owner, &location, &options); err != nil {
			continue
		}
		tss = append(tss, Tablespace{
			Name:     name,
			Owner:    owner,
			Location: location,
			Options:  splitArray(options),
		})
	}
	return tss, rows.Err()
}

func queryDatabases(ctx context.Context, db *sql.DB) ([]DatabaseInfo, error) {
	rows, err := db.QueryContext(ctx,
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
	defer rows.Close()

	var dbs []DatabaseInfo
	for rows.Next() {
		var (
			name, owner, encoding, collate, ctype, defaultTS string
			allowConn, isTemplate                            bool
			connLimit                                        int
		)
		if err := rows.Scan(&name, &owner, &encoding, &collate, &ctype,
			&allowConn, &isTemplate, &connLimit, &defaultTS); err != nil {
			continue
		}
		dbs = append(dbs, DatabaseInfo{
			Name:              name,
			Owner:             owner,
			Encoding:          encoding,
			Collate:           collate,
			CType:             ctype,
			AllowConn:         allowConn,
			IsTemplate:        isTemplate,
			ConnectionLimit:   connLimit,
			DefaultTablespace: defaultTS,
		})
	}
	return dbs, rows.Err()
}

func queryExtensions(ctx context.Context, db *sql.DB, info *DatabaseInfo) error {
	rows, err := db.QueryContext(ctx,
		`SELECT e.extname, e.extversion, n.nspname `+
			`FROM pg_extension e `+
			`JOIN pg_namespace n ON n.oid = e.extnamespace `+
			`ORDER BY e.extname`)
	if err != nil {
		return fmt.Errorf("query extensions for %s: %w", info.Name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, version, schema string
		if err := rows.Scan(&name, &version, &schema); err != nil {
			continue
		}
		info.Extensions = append(info.Extensions, Extension{Name: name, Version: version, Schema: schema})
	}
	return rows.Err()
}

func querySchemas(ctx context.Context, db *sql.DB, info *DatabaseInfo) error {
	rows, err := db.QueryContext(ctx,
		`SELECT nspname, pg_get_userbyid(nspowner) `+
			`FROM pg_namespace `+
			`WHERE nspname NOT IN ('pg_catalog', 'information_schema') `+
			`AND nspname NOT LIKE 'pg_toast%' `+
			`AND nspname NOT LIKE 'pg_temp%' `+
			`ORDER BY nspname`)
	if err != nil {
		return fmt.Errorf("query schemas for %s: %w", info.Name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, owner string
		if err := rows.Scan(&name, &owner); err != nil {
			continue
		}
		info.Schemas = append(info.Schemas, SchemaInfo{Name: name, Owner: owner})
	}
	return rows.Err()
}

func queryRelations(ctx context.Context, db *sql.DB, info *DatabaseInfo) error {
	relRows, err := db.QueryContext(ctx, `
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
		return fmt.Errorf("query relations for %s: %w", info.Name, err)
	}
	defer relRows.Close()

	relIdx := make(map[string]int) // "schema.name" → position in info.Relations
	for relRows.Next() {
		var (
			schema, name, owner, persistence, kind           string
			rowEst, liveRowEst                               int64
			hasPK, hasTriggers, rlsEnabled, rlsForced        bool
			isPartition                                      bool
			storageOpts, partParent, partStrategy, tablespace string
			lastVacuumEpoch, lastAnalyzeEpoch                int64
		)
		if err := relRows.Scan(
			&schema, &name, &owner, &persistence, &kind,
			&rowEst, &liveRowEst,
			&hasPK, &hasTriggers, &rlsEnabled, &rlsForced, &isPartition,
			&storageOpts, &partParent, &partStrategy, &tablespace,
			&lastVacuumEpoch, &lastAnalyzeEpoch,
		); err != nil {
			continue
		}
		rel := RelationInfo{
			Schema:            schema,
			Name:              name,
			Owner:             owner,
			Persistence:       persistence,
			Kind:              kind,
			RowEstimate:       rowEst,
			LiveRowEstimate:   liveRowEst,
			HasPrimaryKey:     hasPK,
			HasTriggers:       hasTriggers,
			RLSEnabled:        rlsEnabled,
			RLSForced:         rlsForced,
			IsPartition:       isPartition,
			StorageOptions:    splitArray(storageOpts),
			PartitionParent:   partParent,
			PartitionStrategy: partStrategy,
			Tablespace:        tablespace,
			LastVacuum:        epochToTime(lastVacuumEpoch),
			LastAnalyze:       epochToTime(lastAnalyzeEpoch),
		}
		relIdx[schema+"."+name] = len(info.Relations)
		info.Relations = append(info.Relations, rel)
	}
	if err := relRows.Err(); err != nil {
		return fmt.Errorf("query relations for %s: %w", info.Name, err)
	}

	// Columns.
	colRows, err := db.QueryContext(ctx, `
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
		return fmt.Errorf("query columns for %s: %w", info.Name, err)
	}
	defer colRows.Close()

	for colRows.Next() {
		var schema, relname, colname, coltype, coldefault string
		var attnum int
		var nullable bool
		if err := colRows.Scan(&schema, &relname, &attnum, &colname, &coltype, &nullable, &coldefault); err != nil {
			continue
		}
		idx, ok := relIdx[schema+"."+relname]
		if !ok {
			continue
		}
		info.Relations[idx].Columns = append(info.Relations[idx].Columns, ColumnInfo{
			Position: attnum,
			Name:     colname,
			Type:     coltype,
			Nullable: nullable,
			Default:  coldefault,
		})
	}
	if err := colRows.Err(); err != nil {
		return fmt.Errorf("query columns for %s: %w", info.Name, err)
	}
	// Set ColumnCount from actual queried columns (more accurate than relnatts).
	for i := range info.Relations {
		info.Relations[i].ColumnCount = len(info.Relations[i].Columns)
	}

	// Constraints (primary key, unique, foreign key, check, exclusion).
	conRows, err := db.QueryContext(ctx, `
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
		return fmt.Errorf("query constraints for %s: %w", info.Name, err)
	}
	defer conRows.Close()

	for conRows.Next() {
		var schema, relname, conname, contype, cols string
		if err := conRows.Scan(&schema, &relname, &conname, &contype, &cols); err != nil {
			continue
		}
		idx, ok := relIdx[schema+"."+relname]
		if !ok {
			continue
		}
		info.Relations[idx].Constraints = append(info.Relations[idx].Constraints, ConstraintInfo{
			Name:    conname,
			Type:    contype,
			Columns: splitArray(cols),
		})
	}
	if err := conRows.Err(); err != nil {
		return fmt.Errorf("query constraints for %s: %w", info.Name, err)
	}

	// Indexes with structured metadata.
	idxRows, err := db.QueryContext(ctx, `
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
		return fmt.Errorf("query indexes for %s: %w", info.Name, err)
	}
	defer idxRows.Close()

	for idxRows.Next() {
		var schema, tablename, indexname, method, definition, constraintName string
		var isUnique, isPrimary, isValid, isPartial bool
		if err := idxRows.Scan(&schema, &tablename, &indexname, &method, &definition,
			&isUnique, &isPrimary, &isValid, &isPartial, &constraintName); err != nil {
			continue
		}
		idx, ok := relIdx[schema+"."+tablename]
		if !ok {
			continue
		}
		info.Relations[idx].Indexes = append(info.Relations[idx].Indexes, IndexInfo{
			Name:           indexname,
			Method:         method,
			Definition:     definition,
			IsUnique:       isUnique,
			IsPrimary:      isPrimary,
			IsValid:        isValid,
			IsPartial:      isPartial,
			ConstraintName: constraintName,
		})
	}
	return idxRows.Err()
}
