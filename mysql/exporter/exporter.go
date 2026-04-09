package exporter

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PlakarKorp/integration-mysql/mysqlconn"
	"github.com/PlakarKorp/kloset/connectors"
	iexporter "github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

// Exporter restores MySQL dumps using the mysql CLI.
type Exporter struct {
	conn     mysqlconn.ConnConfig
	database string // target database; inferred from filename if empty
	createDB bool
	force    bool
}

// New constructs an Exporter from the connector configuration map.
func New(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (iexporter.Exporter, error) {
	conn, err := mysqlconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}

	exp := &Exporter{
		conn:     conn,
		database: mysqlconn.DatabaseFromConfig(config),
	}
	var err2 error
	if exp.createDB, err2 = strconv.ParseBool(config["create_db"]); err2 != nil && config["create_db"] != "" {
		return nil, fmt.Errorf("invalid value for create_db: %w", err2)
	}
	if exp.force, err2 = strconv.ParseBool(config["force"]); err2 != nil && config["force"] != "" {
		return nil, fmt.Errorf("invalid value for force: %w", err2)
	}
	return exp, nil
}

// Origin returns a human-readable destination identifier.
func (e *Exporter) Origin() string {
	if e.database != "" {
		return "mysql://" + e.conn.Host + ":" + e.conn.Port + "/" + e.database
	}
	return "mysql://" + e.conn.Host + ":" + e.conn.Port
}

// Type returns the connector type label.
func (e *Exporter) Type() string { return "mysql" }

// Root returns the root path of the backup.
func (e *Exporter) Root() string { return "/" }

// Flags returns 0 — the exporter is not streaming and can re-read records.
func (e *Exporter) Flags() location.Flags { return 0 }

// Ping verifies connectivity to the MySQL server.
func (e *Exporter) Ping(ctx context.Context) error {
	return e.conn.Ping(ctx)
}

// Close is a no-op for this exporter.
func (e *Exporter) Close(_ context.Context) error { return nil }

// Export processes incoming backup records and restores them to MySQL.
func (e *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	for record := range records {
		result := e.restore(ctx, record)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case results <- result:
		}
	}
	return nil
}

func (e *Exporter) restore(ctx context.Context, record *connectors.Record) *connectors.Result {
	// Skip directories.
	if record.FileInfo.Lmode.IsDir() {
		return record.Ok()
	}
	// Skip the manifest — it is metadata only.
	if record.Pathname == "/manifest.json" {
		return record.Ok()
	}

	if !strings.HasSuffix(record.Pathname, ".sql") {
		return record.Error(fmt.Errorf("unexpected file in MySQL backup: %s", record.Pathname))
	}

	if err := e.restoreSQL(ctx, record); err != nil {
		return record.Error(fmt.Errorf("restoring %s: %w", record.Pathname, err))
	}
	return record.Ok()
}

// restoreSQL pipes a .sql file into the mysql CLI.
func (e *Exporter) restoreSQL(ctx context.Context, record *connectors.Record) error {
	// Determine target database.
	targetDB := e.database
	if targetDB == "" && record.Pathname != "/all.sql" {
		// Infer from filename: "/mydb.sql" → "mydb"
		base := filepath.Base(record.Pathname)
		targetDB = strings.TrimSuffix(base, ".sql")
	}

	// Optionally create the database before restoring.
	if e.createDB && targetDB != "" {
		if err := e.createDatabase(ctx, targetDB); err != nil {
			return fmt.Errorf("creating database %s: %w", targetDB, err)
		}
	}

	args := e.conn.Args()
	if e.force {
		args = append(args, "--force")
	}
	// Pass --batch and --silent to suppress interactive prompts.
	args = append(args, "--batch", "--silent")
	if targetDB != "" {
		args = append(args, targetDB)
	}

	cmd := exec.CommandContext(ctx, e.conn.BinPath("mysql"), args...)
	cmd.Env = e.conn.Env()

	reader := record.Reader
	if reader == nil {
		return fmt.Errorf("record %s has no reader", record.Pathname)
	}
	cmd.Stdin = reader

	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// createDatabase issues CREATE DATABASE IF NOT EXISTS for the target database.
func (e *Exporter) createDatabase(ctx context.Context, database string) error {
	// Validate the database name to prevent injection — MySQL names must not
	// contain backticks.  The mysql CLI will further enforce naming rules.
	if strings.ContainsAny(database, "`\x00") {
		return fmt.Errorf("invalid database name: %q", database)
	}

	args := e.conn.Args()
	args = append(args, "--batch", "--silent",
		"-e", "CREATE DATABASE IF NOT EXISTS `"+database+"`")

	cmd := exec.CommandContext(ctx, e.conn.BinPath("mysql"), args...)
	cmd.Env = e.conn.Env()
	cmd.Stdin = io.NopCloser(strings.NewReader(""))

	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}
