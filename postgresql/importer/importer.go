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
	importer.Register("postgresql", 0, NewImporter)
}

type Importer struct {
	conn       pgconn.ConnConfig
	database   string // empty means back up all databases via pg_dumpall
	compress   bool   // enable pg_dump compression; off by default to avoid degrading Plakar's compression
	schemaOnly bool   // pass -s: dump schema only
	dataOnly   bool   // pass -a: dump data only
	pgBinDir   string // directory containing pg_dump, pg_dumpall, psql; empty means use $PATH
}

// bin returns the full path to a PostgreSQL binary.
// When pgBinDir is empty the name is returned as-is so the OS resolves it via $PATH.
func (p *Importer) bin(name string) string {
	if p.pgBinDir == "" {
		return name
	}
	return filepath.Join(p.pgBinDir, name)
}

func NewImporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	conn, dbPath, err := pgconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}

	imp := &Importer{
		conn:     conn,
		database: dbPath,
	}

	if db, ok := config["database"]; ok && db != "" {
		imp.database = db
	}
	if v, ok := config["pg_bin_dir"]; ok && v != "" {
		imp.pgBinDir = v
	}
	if v, ok := config["compress"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("compress: %w", err)
		}
		imp.compress = b
	}
	if v, ok := config["schema_only"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("schema_only: %w", err)
		}
		imp.schemaOnly = b
	}
	if v, ok := config["data_only"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("data_only: %w", err)
		}
		imp.dataOnly = b
	}
	if imp.schemaOnly && imp.dataOnly {
		return nil, fmt.Errorf("schema_only and data_only are mutually exclusive")
	}

	return imp, nil
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

	if p.database != "" {
		if err := p.emitManifest(ctx, records, "custom"); err != nil {
			return err
		}
		if err := p.dumpGlobals(ctx, records); err != nil {
			return err
		}
		return p.dumpDatabase(ctx, records, p.database)
	}
	if err := p.emitManifest(ctx, records, "sql"); err != nil {
		return err
	}
	return p.dumpAll(ctx, records)
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

// dumpGlobals runs pg_dumpall --globals-only and emits one record named /globals.sql.
// It captures roles and tablespaces that pg_dump does not include.
func (p *Importer) dumpGlobals(ctx context.Context, records chan<- *connectors.Record) error {
	args := p.noRolePasswordsArgs(ctx, append(p.conn.Args(), "--globals-only"))
	return p.emitRecord(ctx, records, p.bin("pg_dumpall"), args, "/globals.sql")
}

// dumpDatabase runs pg_dump -Fc and emits one record named /<dbname>.dump.
func (p *Importer) dumpDatabase(ctx context.Context, records chan<- *connectors.Record, dbname string) error {
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

	return p.emitRecord(ctx, records, p.bin("pg_dump"), args, "/"+dbname+".dump")
}

// dumpAll runs pg_dumpall and emits one record named /all.sql.
func (p *Importer) dumpAll(ctx context.Context, records chan<- *connectors.Record) error {
	args := p.noRolePasswordsArgs(ctx, p.conn.Args())
	if p.schemaOnly {
		args = append(args, "-s")
	} else if p.dataOnly {
		args = append(args, "-a")
	}

	return p.emitRecord(ctx, records, p.bin("pg_dumpall"), args, "/all.sql")
}

// emitRecord starts bin with args and sends a streaming Record on records.
// The record size is 0 because the dump size is not known until the stream is consumed.
func (p *Importer) emitRecord(ctx context.Context, records chan<- *connectors.Record, bin string, args []string, recordPath string) error {
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
	return p.conn.Ping(ctx, p.database)
}

func (p *Importer) Close(ctx context.Context) error { return nil }
func (p *Importer) Root() string                    { return "/" }
func (p *Importer) Origin() string                  { return p.conn.Host }
func (p *Importer) Type() string                    { return "postgresql" }

// Flags returns FLAG_STREAM to signal that Import produces a single streaming
// pass. Without it, the framework calls Import twice — once to enumerate
// records and once to read their contents — which would cause the manifest
// and the dump to be emitted twice.
func (p *Importer) Flags() location.Flags {
	return location.FLAG_STREAM
}
