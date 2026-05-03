package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-postgresql/manifest"
	"github.com/PlakarKorp/integration-postgresql/pgconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

func init() {
	importer.Register("postgres", location.FLAG_STREAM, NewImporter)
}

type Importer struct {
	conn          pgconn.ConnConfig
	database      string // empty means back up all databases
	compress      bool   // enable pg_dump compression; off by default to avoid degrading Plakar's compression
	schemaOnly    bool   // pass -s: dump schema only
	dataOnly      bool   // pass -a: dump data only
	pgBinDir      string // directory containing pg_dump, pg_dumpall, psql; empty means use $PATH
	connType      string // returned by Type(); if empty, "postgresql" is used
	TokenProvider func(context.Context) (string, error) // optional; refreshes conn.Password before each subprocess
}

// bin returns the full path to a PostgreSQL binary.
// When pgBinDir is empty the name is returned as-is so the OS resolves it via $PATH.
func (p *Importer) bin(name string) string {
	if p.pgBinDir == "" {
		return name
	}
	return filepath.Join(p.pgBinDir, name)
}

// NewImporterFromConfigMap creates an Importer from an already-constructed
// ConnConfig (e.g. one whose password was replaced with a fetched token) and
// the raw config map for the remaining option parsing.  connType is the value
// returned by Type(); pass an empty string to use the default "postgresql".
func NewImporterFromConfigMap(conn pgconn.ConnConfig, dbPath, connType string, config map[string]string) (*Importer, error) {
	database := dbPath
	if db, ok := config["database"]; ok && db != "" {
		database = db
	}

	var pgBinDir string
	if v, ok := config["pg_bin_dir"]; ok && v != "" {
		pgBinDir = v
	}

	var (
		compress, schemaOnly, dataOnly bool
		err                            error
	)
	if v, ok := config["compress"]; ok && v != "" {
		compress, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("compress: %w", err)
		}
	}
	if v, ok := config["schema_only"]; ok && v != "" {
		schemaOnly, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("schema_only: %w", err)
		}
	}
	if v, ok := config["data_only"]; ok && v != "" {
		dataOnly, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("data_only: %w", err)
		}
	}
	if schemaOnly && dataOnly {
		return nil, fmt.Errorf("schema_only and data_only are mutually exclusive")
	}

	return &Importer{
		conn:       conn,
		database:   database,
		compress:   compress,
		schemaOnly: schemaOnly,
		dataOnly:   dataOnly,
		pgBinDir:   pgBinDir,
		connType:   connType,
	}, nil
}

func NewImporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	conn, dbPath, err := pgconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}
	return NewImporterFromConfigMap(conn, dbPath, "postgresql", config)
}

func (p *Importer) emitManifest(ctx context.Context, records chan<- *connectors.Record, dumpFormat string) error {
	return manifest.EmitLogicalManifest(ctx, manifest.LogicalConfig{
		PgDumpBin:  p.bin("pg_dump"),
		Conn:       p.conn,
		Database:   p.database,
		DumpFormat: dumpFormat,
		Options: manifest.ManifestOptions{
			SchemaOnly: p.schemaOnly,
			DataOnly:   p.dataOnly,
			Compress:   p.compress,
		},
	}, records)
}

func (p *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	if err := p.refreshToken(ctx); err != nil {
		return err
	}
	if err := p.emitManifest(ctx, records, "custom"); err != nil {
		return err
	}
	return p.dumpAllDatabases(ctx, records)
}

// canReadPgAuthid checks whether the connected user can read pg_authid.
// Superusers can; RDS and other restricted environments cannot.
// The result is used to decide whether to pass --no-role-passwords to pg_dumpall.
//
// Only a "permission denied for … pg_authid" failure returns false.  Any other
// failure (bad credentials, DNS error, server down) returns true so that
// pg_dumpall runs without --no-role-passwords and surfaces the real error itself.
func (p *Importer) canReadPgAuthid(ctx context.Context) bool {
	connectDB := p.database
	if connectDB == "" {
		connectDB = "postgres"
	}
	db, err := p.conn.Open(connectDB)
	if err != nil {
		return true
	}
	defer db.Close()
	var n int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM pg_authid LIMIT 1`).Scan(&n)
	if err == nil {
		return true
	}
	s := err.Error()
	return !(strings.Contains(s, "permission denied") && strings.Contains(s, "pg_authid"))
}

// noRolePasswordsArgs returns --no-role-passwords appended to args when the
// connected user cannot read pg_authid, logging a warning in that case.
func (p *Importer) noRolePasswordsArgs(ctx context.Context, args []string) []string {
	if !p.canReadPgAuthid(ctx) {
		log.Printf("postgresql: cannot read pg_authid (restricted superuser, e.g. RDS); " +
			"role passwords will be omitted from the dump — use a true superuser to include them")
		return append(args, "--no-role-passwords")
	}
	return args
}

// listDatabases returns the names of all databases with datallowconn = true,
// ordered by name.  This matches the set of databases that pg_dumpall dumps.
func (p *Importer) listDatabases(ctx context.Context) ([]string, error) {
	db, err := p.conn.Open("postgres")
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT datname FROM pg_database WHERE datallowconn = true ORDER BY datname`)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan database name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// dumpAllDatabases backs up the cluster by:
//  1. Emitting /00000-globals.sql via pg_dumpall --globals-only (roles, tablespaces).
//  2. Listing all connectable databases (datallowconn = true).
//  3. Emitting one /00001-<name>.dump, /00002-<name>.dump, … per database via
//     pg_dump -Fc, skipping databases that do not match p.database when it is set.
//
// The lexicographic ordering of the filenames guarantees that globals are
// always restored before any database dump.
func (p *Importer) dumpAllDatabases(ctx context.Context, records chan<- *connectors.Record) error {
	// Globals first — skipped for data-only backups since globals are schema-only.
	if !p.dataOnly {
		globalsArgs := p.noRolePasswordsArgs(ctx, append(p.conn.Args(), "--globals-only"))
		if err := p.emitRecord(ctx, records, p.bin("pg_dumpall"), globalsArgs, "/00000-globals.sql"); err != nil {
			return err
		}
	}

	databases, err := p.listDatabases(ctx)
	if err != nil {
		return err
	}

	n := 0
	for _, dbname := range databases {
		if p.database != "" && dbname != p.database {
			continue
		}
		n++
		args := append(p.conn.Args(), "-Fc")
		if !p.compress {
			args = append(args, "-Z0")
		}
		if p.schemaOnly {
			args = append(args, "-s")
		} else if p.dataOnly {
			args = append(args, "-a")
		}
		args = append(args, dbname)
		if err := p.emitRecord(ctx, records, p.bin("pg_dump"), args, fmt.Sprintf("/%05d-%s.dump", n, dbname)); err != nil {
			return err
		}
	}

	if p.database != "" && n == 0 {
		return fmt.Errorf("database %q not found or not connectable", p.database)
	}
	return nil
}

// refreshToken calls the tokenProvider (if set) and updates conn.Password with
// a fresh token.  Called before each subprocess so that short-lived credentials
// (e.g. AWS IAM tokens) are always valid when pg_dump / pg_dumpall starts.
func (p *Importer) refreshToken(ctx context.Context) error {
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

// emitRecord starts bin with args and sends a streaming Record on records.
// The record size is 0 because the dump size is not known until the stream is consumed.
func (p *Importer) emitRecord(ctx context.Context, records chan<- *connectors.Record, bin string, args []string, recordPath string) error {
	if err := p.refreshToken(ctx); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	cmd.Env = p.conn.Env()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s: %w", bin, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", bin, err)
	}

	fileinfo := objects.FileInfo{
		Lname:    path.Base(recordPath),
		Lsize:    0,
		Lmode:    0444,
		LmodTime: time.Time{},
	}

	readerFunc := func() (io.ReadCloser, error) {
		return &cmdReader{cmd: cmd, stdout: stdout, stderr: &stderr}, nil
	}

	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ctx.Err()
	case records <- connectors.NewRecord(recordPath, "", fileinfo, nil, readerFunc):
	}
	return nil
}

// cmdReader wraps a command's stdout pipe and surfaces a non-zero exit status
// as an error when the stream reaches EOF.
type cmdReader struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *bytes.Buffer
}

func (r *cmdReader) Read(p []byte) (int, error) {
	n, err := r.stdout.Read(p)
	if err == io.EOF {
		err := r.cmd.Wait()
		if err != nil {
			return n, fmt.Errorf("%w: %s", err, strings.TrimSpace(r.stderr.String()))
		}
	}
	return n, err
}

func (r *cmdReader) Close() error {
	return nil
}

func (p *Importer) Ping(ctx context.Context) error {
	if err := p.refreshToken(ctx); err != nil {
		return err
	}
	return p.conn.Ping(ctx, p.database)
}

func (p *Importer) Close(ctx context.Context) error { return nil }
func (p *Importer) Root() string                    { return "/" }
func (p *Importer) Origin() string                  { return p.conn.Host }
func (p *Importer) Type() string                    { return p.connType }

// Flags returns FLAG_STREAM to signal that Import produces a single streaming
// pass. Without it, the framework calls Import twice — once to enumerate
// records and once to read their contents — which would cause the manifest
// and the dump to be emitted twice.
func (p *Importer) Flags() location.Flags {
	return location.FLAG_STREAM
}
