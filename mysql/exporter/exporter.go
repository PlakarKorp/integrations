package exporter

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PlakarKorp/integration-mysql/importer"
	"github.com/PlakarKorp/integration-mysql/mysqlconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/location"
)

type Exporter struct {
	proto          string // registered protocol, e.g. "mysql" or "mysql+mariadb"
	conn           mysqlconn.ConnConfig
	database       string // target database; inferred from filename if empty
	createDB       bool
	force          bool
	expectedFlavor string // "mysql" or "mariadb"
}

// New constructs an Exporter. conn.ClientBin must be set by the caller.
// flavor is verified against the live server at the start of Export.
func New(proto string, conn mysqlconn.ConnConfig, config map[string]string, flavor string) (*Exporter, error) {
	exp := &Exporter{
		proto:          proto,
		conn:           conn,
		database:       mysqlconn.DatabaseFromConfig(config),
		expectedFlavor: flavor,
	}
	var err error
	if exp.createDB, err = strconv.ParseBool(config["create_db"]); err != nil && config["create_db"] != "" {
		return nil, fmt.Errorf("invalid value for create_db: %w", err)
	}
	if exp.force, err = strconv.ParseBool(config["force"]); err != nil && config["force"] != "" {
		return nil, fmt.Errorf("invalid value for force: %w", err)
	}
	return exp, nil
}

func (e *Exporter) Origin() string {
	if e.database != "" {
		return e.proto + "://" + e.conn.Host + ":" + e.conn.Port + "/" + e.database
	}
	return e.proto + "://" + e.conn.Host + ":" + e.conn.Port
}

func (e *Exporter) Type() string                   { return e.proto }
func (e *Exporter) Root() string                   { return "/" }
func (e *Exporter) Flags() location.Flags          { return 0 }
func (e *Exporter) Ping(ctx context.Context) error { return e.conn.Ping(ctx) }
func (e *Exporter) Close(_ context.Context) error  { return nil }

func (e *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)
	if err := e.conn.CheckFlavor(ctx, e.expectedFlavor); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case record, ok := <-records:
			if !ok {
				return nil
			}
			results <- e.restore(ctx, record)
		}
	}
}

func (e *Exporter) restore(ctx context.Context, record *connectors.Record) *connectors.Result {
	if record.FileInfo.Lmode.IsDir() {
		return record.Ok()
	}
	if record.Pathname == "manifest.json" { // metadata only, nothing to restore
		return record.Ok()
	}

	if !strings.HasSuffix(record.Pathname, ".sql") {
		return record.Error(fmt.Errorf("unexpected file in backup: %s", record.Pathname))
	}

	if err := e.restoreSQL(ctx, record); err != nil {
		return record.Error(fmt.Errorf("restoring %s: %w", record.Pathname, err))
	}
	return record.Ok()
}

func (e *Exporter) restoreSQL(ctx context.Context, record *connectors.Record) error {
	targetDB := e.database
	if targetDB == "" && record.Pathname != path.Base(importer.AllDatabasesDumpFile) {
		// Infer from filename: "mydb.sql" → "mydb".
		base := filepath.Base(record.Pathname)
		targetDB = strings.TrimSuffix(base, ".sql")
	}

	if e.createDB && targetDB != "" {
		if err := e.createDatabase(ctx, targetDB); err != nil {
			return fmt.Errorf("creating database %s: %w", targetDB, err)
		}
	}

	pwArg, cleanup, err := e.conn.PasswordFileArg()
	if err != nil {
		return err
	}
	defer cleanup()

	var extra []string
	if e.force {
		extra = append(extra, "--force")
	}
	extra = append(extra, "--batch", "--silent")
	if targetDB != "" {
		extra = append(extra, targetDB)
	}
	args := e.conn.ArgsWithPassword(pwArg, extra...)

	cmd := exec.CommandContext(ctx, e.conn.BinPath(e.conn.ClientBin), args...)
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

func (e *Exporter) createDatabase(ctx context.Context, database string) error {
	if strings.ContainsAny(database, "`\x00") { // prevent injection via backtick or NUL
		return fmt.Errorf("invalid database name: %q", database)
	}

	pwArg, cleanup, err := e.conn.PasswordFileArg()
	if err != nil {
		return err
	}
	defer cleanup()

	args := e.conn.ArgsWithPassword(pwArg,
		"--batch", "--silent",
		"-e", "CREATE DATABASE IF NOT EXISTS `"+database+"`")

	cmd := exec.CommandContext(ctx, e.conn.BinPath(e.conn.ClientBin), args...)
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
