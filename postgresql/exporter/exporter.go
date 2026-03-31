package exporter

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PlakarKorp/integration-postgresql/pgconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	exporter.Register("postgresql", 0, NewExporter)
}

type Exporter struct {
	conn           pgconn.ConnConfig
	database       string // target database; if empty, inferred from dump filename
	noOwner        bool   // pass --no-owner to pg_restore
	exitOnError    bool   // pass -e to pg_restore / ON_ERROR_STOP=1 to psql
	createDB       bool   // pass -C to pg_restore: create the database from archive metadata
	schemaOnly     bool   // pass -s to pg_restore
	dataOnly       bool   // pass -a to pg_restore
	restoreGlobals bool   // feed globals.sql to psql before restoring the database
	pgRestoreBin   string
	psqlBin        string
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	conn, dbPath, err := pgconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}

	exp := &Exporter{
		conn:         conn,
		database:     dbPath,
		pgRestoreBin: "pg_restore",
		psqlBin:      "psql",
	}

	if db, ok := config["database"]; ok && db != "" {
		exp.database = db
	}
	if v, ok := config["no_owner"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("no_owner: %w", err)
		}
		exp.noOwner = b
	}
	if v, ok := config["exit_on_error"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("exit_on_error: %w", err)
		}
		exp.exitOnError = b
	}
	if v, ok := config["create_db"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("create_db: %w", err)
		}
		exp.createDB = b
	}
	if v, ok := config["restore_globals"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("restore_globals: %w", err)
		}
		exp.restoreGlobals = b
	}
	if v, ok := config["schema_only"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("schema_only: %w", err)
		}
		exp.schemaOnly = b
	}
	if v, ok := config["data_only"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("data_only: %w", err)
		}
		exp.dataOnly = b
	}
	if exp.schemaOnly && exp.dataOnly {
		return nil, fmt.Errorf("schema_only and data_only are mutually exclusive")
	}
	if v, ok := config["pg_restore"]; ok && v != "" {
		exp.pgRestoreBin = v
	}
	if v, ok := config["psql"]; ok && v != "" {
		exp.psqlBin = v
	}
	return exp, nil
}

func (p *Exporter) Root() string          { return "/" }
func (p *Exporter) Origin() string        { return p.conn.Host }
func (p *Exporter) Type() string          { return "postgresql" }
func (p *Exporter) Flags() location.Flags { return 0 }

func (p *Exporter) Ping(ctx context.Context) error {
	return p.conn.Ping(ctx, p.psqlBin, p.database)
}

func (p *Exporter) Close(ctx context.Context) error {
	return nil
}

// Export restores each record to the configured PostgreSQL server.
// Records ending in ".dump" are restored via pg_restore (pg_dump custom format).
// Records ending in ".sql" are fed to psql (pg_dumpall plain-SQL format).
func (p *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

loop:
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case record, ok := <-records:
			if !ok {
				break loop
			}

			if record.Err != nil {
				results <- record.Ok()
				continue
			}

			if record.IsXattr {
				results <- record.Ok()
				continue
			}

			if record.FileInfo.Lmode.IsDir() {
				results <- record.Ok()
				continue
			}

			if err := p.restore(ctx, record); err != nil {
				results <- record.Error(err)
			} else {
				results <- record.Ok()
			}
		}
	}

	return nil
}

func (p *Exporter) restore(ctx context.Context, record *connectors.Record) error {
	if strings.HasSuffix(record.Pathname, ".dump") {
		return p.pgRestore(ctx, record.Reader, record.Pathname)
	}
	if record.Pathname == "/globals.sql" {
		if p.restoreGlobals {
			return p.psqlRestore(ctx, record.Reader)
		}
		return nil
	}
	if strings.HasSuffix(record.Pathname, ".sql") {
		return p.psqlRestore(ctx, record.Reader)
	}
	// Ignore unknown files (e.g. manifest.json).
	return nil
}

// pgRestore restores a pg_dump custom-format dump via pg_restore.
// With create_db: -C creates the database from the archive metadata and
// -d is used only for the initial maintenance-database connection.
// Without create_db: the target database must already exist.
func (p *Exporter) pgRestore(ctx context.Context, r io.Reader, pathname string) error {
	var args []string
	if p.createDB {
		connectDB := p.database
		if connectDB == "" {
			connectDB = "postgres"
		}
		args = []string{"-h", p.conn.Host, "-p", p.conn.Port, "-w", "-C", "-d", connectDB}
	} else {
		targetDB := p.database
		if targetDB == "" {
			targetDB = strings.TrimSuffix(filepath.Base(pathname), ".dump")
		}
		args = []string{"-h", p.conn.Host, "-p", p.conn.Port, "-w", "-d", targetDB}
	}
	if p.exitOnError {
		args = append(args, "-e")
	}
	if p.noOwner {
		args = append(args, "--no-owner")
	}
	if p.schemaOnly {
		args = append(args, "-s")
	} else if p.dataOnly {
		args = append(args, "-a")
	}
	if p.conn.Username != "" {
		args = append(args, "-U", p.conn.Username)
	}

	cmd := exec.CommandContext(ctx, p.pgRestoreBin, args...)
	cmd.Stdin = r
	cmd.Env = p.conn.Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_restore: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// psqlRestore feeds a pg_dumpall plain-SQL dump to psql, connecting to the
// "postgres" maintenance database so the script can recreate other databases.
func (p *Exporter) psqlRestore(ctx context.Context, r io.Reader) error {
	args := []string{"-h", p.conn.Host, "-p", p.conn.Port, "-w", "-d", "postgres"}
	if p.exitOnError {
		args = append(args, "-v", "ON_ERROR_STOP=1")
	}
	if p.conn.Username != "" {
		args = append(args, "-U", p.conn.Username)
	}

	cmd := exec.CommandContext(ctx, p.psqlBin, args...)
	cmd.Stdin = r
	cmd.Env = p.conn.Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("psql: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
