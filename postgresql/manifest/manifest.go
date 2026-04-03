package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
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
	PSQLBin    string
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
	sv, svNum, err := serverVersion(ctx, cfg.PSQLBin, cfg.Conn, connectDB)
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
	collectClusterMetadata(ctx, cfg.PSQLBin, cfg.Conn, connectDB, m)

	if cfg.Database != "" {
		// Single-database backup: only detail the target database.
		for i := range m.Databases {
			if m.Databases[i].Name == cfg.Database {
				_ = queryDatabaseDetail(ctx, cfg.PSQLBin, cfg.Conn, &m.Databases[i])
				m.Databases = []DatabaseInfo{m.Databases[i]}
				break
			}
		}
	} else {
		// Full-cluster backup: detail every connectable non-template database.
		for i := range m.Databases {
			if m.Databases[i].AllowConn && !m.Databases[i].IsTemplate {
				_ = queryDatabaseDetail(ctx, cfg.PSQLBin, cfg.Conn, &m.Databases[i])
			}
		}
	}

	return emitManifest(ctx, records, m)
}

// PhysicalConfig holds the parameters needed to build and emit a physical
// (pg_basebackup) backup manifest.
type PhysicalConfig struct {
	PSQLBin         string
	PgBaseBackupBin string
	Conn            pgconn.ConnConfig
}

// EmitPhysicalManifest builds the manifest for a physical backup and sends it
// on records.  It queries full cluster and per-database metadata (the physical
// backup captures all databases at the file level, so relation detail is still
// useful for inventory purposes).
func EmitPhysicalManifest(ctx context.Context, cfg PhysicalConfig, records chan<- *connectors.Record) error {
	sv, svNum, err := serverVersion(ctx, cfg.PSQLBin, cfg.Conn, "postgres")
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
	collectClusterMetadata(ctx, cfg.PSQLBin, cfg.Conn, "postgres", m)

	for i := range m.Databases {
		if m.Databases[i].AllowConn && !m.Databases[i].IsTemplate {
			_ = queryDatabaseDetail(ctx, cfg.PSQLBin, cfg.Conn, &m.Databases[i])
		}
	}

	return emitManifest(ctx, records, m)
}

// serverVersion queries the PostgreSQL server for its version string and
// numeric version. psqlBin is the path to the psql binary; connectDB is
// the database used for the connection.
func serverVersion(ctx context.Context, psqlBin string, conn pgconn.ConnConfig, connectDB string) (string, int, error) {
	args := append(conn.Args(),
		"-d", connectDB,
		"-t", "-A", "-F", "|", "--no-psqlrc",
		"-c", "SELECT current_setting('server_version'), current_setting('server_version_num')",
	)
	cmd := exec.CommandContext(ctx, psqlBin, args...)
	cmd.Stdin = nil
	cmd.Env = conn.Env()
	out, err := cmd.Output()
	if err != nil {
		return "", 0, fmt.Errorf("querying server version: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("unexpected server version output: %q", string(out))
	}
	versionNum, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("parsing server_version_num %q: %w", parts[1], err)
	}
	return parts[0], versionNum, nil
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
