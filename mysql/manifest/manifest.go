package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-mysql/mysqlconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

// ManifestOptions records which dump options were active when the backup was created.
type ManifestOptions struct {
	NoData            bool   `json:"no_data"`
	NoCreateInfo      bool   `json:"no_create_info"`
	NoTablespaces     bool   `json:"no_tablespaces"`
	ColumnStatistics  bool   `json:"column_statistics"`
	SingleTransaction bool   `json:"single_transaction"`
	Routines          bool   `json:"routines"`
	Events            bool   `json:"events"`
	Triggers          bool   `json:"triggers"`
	HexBlob           bool   `json:"hex_blob"`
	SetGTIDPurged     string `json:"set_gtid_purged,omitempty"`
}

// Manifest is the top-level structure serialised to /manifest.json.
type Manifest struct {
	Version          int              `json:"version"`
	CreatedAt        time.Time        `json:"created_at"`
	Connector        string           `json:"connector"`
	Host             string           `json:"host"`
	Port             string           `json:"port"`
	ServerVersion    string           `json:"server_version"`
	MysqldumpVersion string           `json:"mysqldump_version,omitempty"`
	IsReadReplica    bool             `json:"is_read_replica"`
	Database         string           `json:"database,omitempty"` // empty = all databases
	DumpFormat       string           `json:"dump_format"`        // always "sql" for this plugin
	Options          *ManifestOptions `json:"options,omitempty"`
	ServerConfig     *ServerConfig    `json:"server_config,omitempty"`
	Users            []UserInfo       `json:"users,omitempty"`
	Databases        []DatabaseInfo   `json:"databases,omitempty"`
}

// Config holds all parameters needed to build and emit a logical backup manifest.
type Config struct {
	Conn     mysqlconn.ConnConfig
	Database string // empty = all databases (--all-databases)
	Options  ManifestOptions
}

// Emit builds the manifest, connects to MySQL to collect metadata, and sends
// /manifest.json as the first record on the records channel.
// Individual metadata query failures are non-fatal — partial manifests are
// acceptable and never abort a backup.
func Emit(ctx context.Context, cfg Config, records chan<- *connectors.Record) error {
	connectDB := cfg.Database
	if connectDB == "" {
		connectDB = "information_schema"
	}

	db, err := openDB(cfg.Conn, connectDB)
	if err != nil {
		return fmt.Errorf("manifest: connecting to MySQL: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("manifest: pinging MySQL: %w", err)
	}

	sv, err := serverVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("manifest: querying server version: %w", err)
	}

	m := &Manifest{
		Version:       1,
		CreatedAt:     time.Now().UTC(),
		Connector:     "mysql",
		Host:          cfg.Conn.Host,
		Port:          cfg.Conn.Port,
		ServerVersion: sv,
		Database:      cfg.Database,
		DumpFormat:    "sql",
		IsReadReplica: isReadReplica(ctx, db),
		Options: &ManifestOptions{
			NoData:            cfg.Options.NoData,
			NoCreateInfo:      cfg.Options.NoCreateInfo,
			NoTablespaces:     cfg.Options.NoTablespaces,
			ColumnStatistics:  cfg.Options.ColumnStatistics,
			SingleTransaction: cfg.Options.SingleTransaction,
			Routines:          cfg.Options.Routines,
			Events:            cfg.Options.Events,
			Triggers:          cfg.Options.Triggers,
			HexBlob:           cfg.Options.HexBlob,
			SetGTIDPurged:     cfg.Options.SetGTIDPurged,
		},
	}

	m.MysqldumpVersion = mysqldumpBinVersion(cfg.Conn.BinPath("mysqldump"))

	// Collect server configuration (best-effort).
	if sc, err := collectServerConfig(ctx, db); err == nil {
		m.ServerConfig = &sc
	}

	// Collect users (best-effort — may fail without mysql.user access).
	if users, err := collectUsers(ctx, db); err == nil {
		m.Users = users
	}

	// Collect database list.
	dbs, err := collectDatabases(ctx, db, cfg.Database)
	if err == nil {
		// Enrich each database with table/routine/trigger/event detail.
		for i := range dbs {
			dbConn, connErr := openDB(cfg.Conn, dbs[i].Name)
			if connErr != nil {
				continue
			}
			collectDatabaseDetail(ctx, dbConn, &dbs[i])
			dbConn.Close()
		}
		m.Databases = dbs
	}

	return emitManifest(ctx, records, m)
}

// mysqldumpBinVersion returns the version string from mysqldump --version.
func mysqldumpBinVersion(binPath string) string {
	out, err := exec.Command(binPath, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// emitManifest serialises m as /manifest.json and sends it on records.
func emitManifest(ctx context.Context, records chan<- *connectors.Record, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest: marshalling JSON: %w", err)
	}

	now := time.Now().UTC()
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
