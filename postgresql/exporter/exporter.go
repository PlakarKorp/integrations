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
	exporter.Register("postgres", 0, NewExporter)
}

type Exporter struct {
	conn             pgconn.ConnConfig
	database         string // target database; if empty, inferred from dump filename
	noOwner          bool   // pass --no-owner to pg_restore
	exitOnError      bool   // pass -e to pg_restore / ON_ERROR_STOP=1 to psql
	clean            bool   // pass --clean --if-exists to pg_restore: drop objects before recreating
	dropAndRecreate  bool   // pass -C --clean --if-exists: drop the database and recreate it from archive metadata
	schemaOnly       bool   // pass -s to pg_restore
	dataOnly         bool   // pass -a to pg_restore
	noGlobals        bool   // skip feeding globals.sql / 00000-globals.sql to psql
	pgBinDir         string // directory containing pg_restore, psql; empty means use $PATH
	connType         string // returned by Type()
	TokenProvider    func(context.Context) (string, error) // optional; refreshes conn.Password before each subprocess
}

// bin returns the full path to a PostgreSQL binary.
// When pgBinDir is empty the name is returned as-is so the OS resolves it via $PATH.
func (p *Exporter) bin(name string) string {
	if p.pgBinDir == "" {
		return name
	}
	return filepath.Join(p.pgBinDir, name)
}

// NewExporterFromConfigMap creates an Exporter from an already-constructed
// ConnConfig (e.g. one whose password was replaced with a fetched token) and
// the raw config map for the remaining option parsing.  connType is the value
// returned by Type().
func NewExporterFromConfigMap(conn pgconn.ConnConfig, dbPath, connType string, config map[string]string) (*Exporter, error) {
	exp := &Exporter{
		conn:     conn,
		database: dbPath,
		connType: connType,
	}

	var err error
	if db, ok := config["database"]; ok && db != "" {
		exp.database = db
	}
	if v, ok := config["no_owner"]; ok && v != "" {
		exp.noOwner, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("no_owner: %w", err)
		}
	}
	if v, ok := config["exit_on_error"]; ok && v != "" {
		exp.exitOnError, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("exit_on_error: %w", err)
		}
	}
	if v, ok := config["clean"]; ok && v != "" {
		exp.clean, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("clean: %w", err)
		}
	}
	if v, ok := config["drop_and_recreate"]; ok && v != "" {
		exp.dropAndRecreate, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("drop_and_recreate: %w", err)
		}
	}
	if exp.clean && exp.dropAndRecreate {
		return nil, fmt.Errorf("clean and drop_and_recreate are mutually exclusive")
	}
	if v, ok := config["no_globals"]; ok && v != "" {
		exp.noGlobals, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("no_globals: %w", err)
		}
	}
	if v, ok := config["schema_only"]; ok && v != "" {
		exp.schemaOnly, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("schema_only: %w", err)
		}
	}
	if v, ok := config["data_only"]; ok && v != "" {
		exp.dataOnly, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("data_only: %w", err)
		}
	}
	if exp.schemaOnly && exp.dataOnly {
		return nil, fmt.Errorf("schema_only and data_only are mutually exclusive")
	}
	if v, ok := config["pg_bin_dir"]; ok && v != "" {
		exp.pgBinDir = v
	}
	return exp, nil
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	conn, dbPath, err := pgconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}
	return NewExporterFromConfigMap(conn, dbPath, "postgresql", config)
}

func (p *Exporter) Root() string          { return "/" }
func (p *Exporter) Origin() string        { return p.conn.Host }
func (p *Exporter) Type() string          { return p.connType }
func (p *Exporter) Flags() location.Flags { return 0 }

func (p *Exporter) Ping(ctx context.Context) error {
	return p.conn.Ping(ctx, p.database)
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
				results <- record.Error(fmt.Errorf("unexpected xattr %q", record.Pathname))
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
	// manifest.json is metadata only; nothing to restore.
	if record.Pathname == "manifest.json" {
		return nil
	}
	if strings.HasSuffix(record.Pathname, ".dump") {
		return p.pgRestore(ctx, record.Reader, record.Pathname)
	}
	if record.Pathname == "globals.sql" || record.Pathname == "00000-globals.sql" {
		if !p.noGlobals {
			return p.psqlRestore(ctx, record.Reader)
		}
		return nil
	}
	if strings.HasSuffix(record.Pathname, ".sql") {
		return p.psqlRestore(ctx, record.Reader)
	}
	return fmt.Errorf("unable to restore file %q", record.Pathname)
}

// refreshToken calls the tokenProvider (if set) and updates conn.Password with
// a fresh token.  Called before each subprocess so that short-lived credentials
// (e.g. AWS IAM tokens) are always valid when pg_restore / psql starts.
func (p *Exporter) refreshToken(ctx context.Context) error {
	if p.TokenProvider == nil {
		return nil
	}
	token, err := p.TokenProvider(ctx)
	if err != nil {
		return fmt.Errorf("refresh token: %w", err)
	}
	p.conn.Password = token
	return nil
}

// pgRestore restores a pg_dump custom-format dump via pg_restore.
//
// Default: restores objects into an existing database (-d <targetdb>).
// clean=true: drops objects within the database before recreating them
// (-d <targetdb> --clean --if-exists); the database itself must already exist.
// drop_and_recreate=true: drops the whole database and recreates it from the
// archive metadata (-C --clean --if-exists -d postgres); the target database
// name is taken from the archive, not from the database parameter.
func (p *Exporter) pgRestore(ctx context.Context, r io.Reader, pathname string) error {
	if err := p.refreshToken(ctx); err != nil {
		return err
	}
	args := p.conn.Args()
	if p.dropAndRecreate {
		args = append(args, "-C", "--clean", "--if-exists", "-d", "postgres")
	} else {
		targetDB := p.database
		if targetDB == "" {
			base := strings.TrimSuffix(filepath.Base(pathname), ".dump")
			// Strip leading numeric prefix from all-databases backup naming
			// (e.g. "00001-mydb" → "mydb").
			if idx := strings.Index(base, "-"); idx > 0 {
				allDigits := true
				for _, c := range base[:idx] {
					if c < '0' || c > '9' {
						allDigits = false
						break
					}
				}
				if allDigits {
					base = base[idx+1:]
				}
			}
			targetDB = base
		}
		args = append(args, "-d", targetDB)
		if p.clean {
			args = append(args, "--clean", "--if-exists")
		}
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

	cmd := exec.CommandContext(ctx, p.bin("pg_restore"), args...)
	cmd.Stdin = r
	cmd.Env = p.conn.Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_restore: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// psqlRestore feeds a plain-SQL dump to psql, connecting to the "postgres"
// maintenance database so the script can recreate other databases.
func (p *Exporter) psqlRestore(ctx context.Context, r io.Reader) error {
	if err := p.refreshToken(ctx); err != nil {
		return err
	}
	args := append(p.conn.Args(), "-d", "postgres")
	if p.exitOnError {
		args = append(args, "-v", "ON_ERROR_STOP=1")
	}

	cmd := exec.CommandContext(ctx, p.bin("psql"), args...)
	cmd.Stdin = r
	cmd.Env = p.conn.Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("psql: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
