package manifest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-postgresql/pgconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

type ManifestOptions struct {
	SchemaOnly bool `json:"schema_only"`
	DataOnly   bool `json:"data_only"`
	Compress   bool `json:"compress"`
}

type Manifest struct {
	Version                 int              `json:"version"`
	CreatedAt               time.Time        `json:"created_at"`
	Connector               string           `json:"connector"`
	Host                    string           `json:"host"`
	Port                    string           `json:"port"`
	ServerVersion           string           `json:"server_version"`
	ServerVersionNum        int              `json:"server_version_num"`
	PgDumpVersion           string           `json:"pg_dump_version,omitempty"`
	PgBaseBackupVersion     string           `json:"pg_basebackup_version,omitempty"`
	ClusterSystemIdentifier string           `json:"cluster_system_identifier,omitempty"`
	InRecovery              bool             `json:"in_recovery"`
	Database                string           `json:"database,omitempty"`
	DumpFormat              string           `json:"dump_format"`
	Options                 *ManifestOptions `json:"options,omitempty"`
	ClusterConfig           *ClusterConfig   `json:"cluster_config,omitempty"`
	Roles                   []Role           `json:"roles,omitempty"`
	Tablespaces             []Tablespace     `json:"tablespaces,omitempty"`
	Databases               []DatabaseInfo   `json:"databases,omitempty"`
}

// LogicalConfig holds the parameters needed to build and emit a logical
// (pg_dump / pg_dumpall) backup manifest.
type LogicalConfig struct {
	PgDumpBin  string
	Conn       pgconn.ConnConfig
	Database   string // empty = full-cluster backup
	DumpFormat string
	Options    ManifestOptions
}

// EmitLogicalManifest builds the manifest for a logical backup and sends it
// on records.  It connects to the target database (or "postgres" for a
// full-cluster backup), queries cluster and per-database metadata, and emits
// /manifest.json.
func EmitLogicalManifest(ctx context.Context, cfg LogicalConfig, records chan<- *connectors.Record) error {
	connectDB := cfg.Database
	if connectDB == "" {
		connectDB = "postgres"
	}

	db, err := cfg.Conn.Open(connectDB)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", connectDB, err)
	}
	defer db.Close()

	sv, svNum, err := serverVersion(ctx, db)
	if err != nil {
		return err
	}

	m := &Manifest{
		Connector:        "postgresql",
		Host:             cfg.Conn.Host,
		Port:             cfg.Conn.Port,
		ServerVersion:    sv,
		ServerVersionNum: svNum,
		Database:         cfg.Database,
		DumpFormat:       cfg.DumpFormat,
		Options: &ManifestOptions{
			SchemaOnly: cfg.Options.SchemaOnly,
			DataOnly:   cfg.Options.DataOnly,
			Compress:   cfg.Options.Compress,
		},
	}

	m.PgDumpVersion = pgDumpVersion(cfg.PgDumpBin)
	collectClusterMetadata(ctx, db, m)

	if cfg.Database != "" {
		// Single-database backup: only detail the target database.
		for i := range m.Databases {
			if m.Databases[i].Name == cfg.Database {
				_ = queryDatabaseDetail(ctx, cfg.Conn, &m.Databases[i])
				m.Databases = []DatabaseInfo{m.Databases[i]}
				break
			}
		}
	} else {
		// Full-cluster backup: detail every connectable non-template database.
		for i := range m.Databases {
			if m.Databases[i].AllowConn && !m.Databases[i].IsTemplate {
				_ = queryDatabaseDetail(ctx, cfg.Conn, &m.Databases[i])
			}
		}
	}

	return emitManifest(ctx, records, m)
}

// PhysicalConfig holds the parameters needed to build and emit a physical
// (pg_basebackup) backup manifest.
type PhysicalConfig struct {
	PgBaseBackupBin string
	Conn            pgconn.ConnConfig
}

// EmitPhysicalManifest builds the manifest for a physical backup and sends it
// on records.  It queries full cluster and per-database metadata (the physical
// backup captures all databases at the file level, so relation detail is still
// useful for inventory purposes).
func EmitPhysicalManifest(ctx context.Context, cfg PhysicalConfig, records chan<- *connectors.Record) error {
	db, err := cfg.Conn.Open("postgres")
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer db.Close()

	sv, svNum, err := serverVersion(ctx, db)
	if err != nil {
		return err
	}

	m := &Manifest{
		Connector:        "postgresql+bin",
		Host:             cfg.Conn.Host,
		Port:             cfg.Conn.Port,
		ServerVersion:    sv,
		ServerVersionNum: svNum,
		DumpFormat:       "basebackup",
	}

	m.PgBaseBackupVersion = pgBaseBackupVersion(cfg.PgBaseBackupBin)
	collectClusterMetadata(ctx, db, m)

	for i := range m.Databases {
		if m.Databases[i].AllowConn && !m.Databases[i].IsTemplate {
			_ = queryDatabaseDetail(ctx, cfg.Conn, &m.Databases[i])
		}
	}

	return emitManifest(ctx, records, m)
}

// serverVersion queries the PostgreSQL server for its version string and
// numeric version using a live *sql.DB connection.
func serverVersion(ctx context.Context, db *sql.DB) (string, int, error) {
	var sv string
	var svNum int
	err := db.QueryRowContext(ctx,
		`SELECT current_setting('server_version'), current_setting('server_version_num')::int`,
	).Scan(&sv, &svNum)
	if err != nil {
		return "", 0, fmt.Errorf("querying server version: %w", err)
	}
	return sv, svNum, nil
}

// emitManifest serialises m as /manifest.json and sends it on records.
func emitManifest(ctx context.Context, records chan<- *connectors.Record, m *Manifest) error {
	m.Version = 2
	now := time.Now().UTC()
	m.CreatedAt = now

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	fileinfo := objects.FileInfo{
		Lname:    "manifest.json",
		Lsize:    int64(len(data)),
		Lmode:    0444,
		LmodTime: now,
	}
	readerFunc := func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- connectors.NewRecord("/manifest.json", "", fileinfo, nil, readerFunc):
	}
	return nil
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
