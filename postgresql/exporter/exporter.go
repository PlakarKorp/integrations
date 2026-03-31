package exporter

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	exporter.Register("postgresql", 0, NewExporter)
}

type Exporter struct {
	host         string
	port         string
	username     string
	password     string
	database     string // target database; if empty, inferred from dump filename
	noOwner      bool   // pass --no-owner to pg_restore
	exitOnError  bool   // pass -e to pg_restore / ON_ERROR_STOP=1 to psql
	pgRestoreBin string
	psqlBin      string
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	exp := &Exporter{
		host:         "localhost",
		port:         "5432",
		pgRestoreBin: "pg_restore",
		psqlBin:      "psql",
	}

	if loc, ok := config["location"]; ok && loc != "" {
		u, err := url.Parse(loc)
		if err != nil {
			return nil, fmt.Errorf("invalid location: %w", err)
		}
		if u.Hostname() != "" {
			exp.host = u.Hostname()
		}
		if u.Port() != "" {
			exp.port = u.Port()
		}
		if u.User != nil {
			if u.User.Username() != "" {
				exp.username = u.User.Username()
			}
			if p, ok := u.User.Password(); ok {
				exp.password = p
			}
		}
		if u.Path != "" && u.Path != "/" {
			exp.database = strings.TrimPrefix(u.Path, "/")
		}
	}

	// Standalone fields override URI components.
	if h, ok := config["host"]; ok && h != "" {
		exp.host = h
	}
	if p, ok := config["port"]; ok && p != "" {
		exp.port = p
	}
	if u, ok := config["username"]; ok && u != "" {
		exp.username = u
	}
	if p, ok := config["password"]; ok && p != "" {
		exp.password = p
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
	if v, ok := config["pg_restore"]; ok && v != "" {
		exp.pgRestoreBin = v
	}
	if v, ok := config["psql"]; ok && v != "" {
		exp.psqlBin = v
	}
	return exp, nil
}

func (p *Exporter) pgEnv() []string {
	env := os.Environ()
	if p.password != "" {
		env = append(env, "PGPASSWORD="+p.password)
	}
	return env
}

func (p *Exporter) Root() string          { return "/" }
func (p *Exporter) Origin() string        { return p.host }
func (p *Exporter) Type() string          { return "postgresql" }
func (p *Exporter) Flags() location.Flags { return 0 }

func (p *Exporter) Ping(ctx context.Context) error {
	connectDB := p.database
	if connectDB == "" {
		connectDB = "postgres"
	}
	args := []string{"-h", p.host, "-p", p.port, "-d", connectDB, "-w", "-c", "SELECT 1", "-q", "--no-psqlrc"}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}
	cmd := exec.CommandContext(ctx, p.psqlBin, args...)
	cmd.Stdin = nil
	cmd.Env = p.pgEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ping: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
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
	return p.psqlRestore(ctx, record.Reader)
}

// pgRestore restores a pg_dump custom-format dump via pg_restore.
// The target database is created if it does not exist.
func (p *Exporter) pgRestore(ctx context.Context, r io.Reader, pathname string) error {
	targetDB := p.database
	if targetDB == "" {
		targetDB = strings.TrimSuffix(filepath.Base(pathname), ".dump")
	}
	if err := p.ensureDatabase(ctx, targetDB); err != nil {
		return err
	}

	args := []string{"-h", p.host, "-p", p.port, "-w", "-d", targetDB}
	if p.exitOnError {
		args = append(args, "-e")
	}
	if p.noOwner {
		args = append(args, "--no-owner")
	}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}

	cmd := exec.CommandContext(ctx, p.pgRestoreBin, args...)
	cmd.Stdin = r
	cmd.Env = p.pgEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_restore: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ensureDatabase creates dbname if it does not already exist.
// The SELECT ... \gexec idiom executes CREATE DATABASE only when the row is
// returned (i.e. the database is absent), so no error is raised if it exists.
func (p *Exporter) ensureDatabase(ctx context.Context, dbname string) error {
	ident := strings.ReplaceAll(dbname, `"`, `""`)
	lit := strings.ReplaceAll(dbname, `'`, `''`)
	query := fmt.Sprintf(
		`SELECT 'CREATE DATABASE "%s"' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '%s')\gexec`,
		ident, lit,
	)
	args := []string{"-h", p.host, "-p", p.port, "-w", "-d", "postgres", "--no-psqlrc"}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}
	cmd := exec.CommandContext(ctx, p.psqlBin, args...)
	cmd.Stdin = strings.NewReader(query)
	cmd.Env = p.pgEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create database %q: %w: %s", dbname, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// psqlRestore feeds a pg_dumpall plain-SQL dump to psql, connecting to the
// "postgres" maintenance database so the script can recreate other databases.
func (p *Exporter) psqlRestore(ctx context.Context, r io.Reader) error {
	args := []string{"-h", p.host, "-p", p.port, "-w", "-d", "postgres"}
	if p.exitOnError {
		args = append(args, "-v", "ON_ERROR_STOP=1")
	}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}

	cmd := exec.CommandContext(ctx, p.psqlBin, args...)
	cmd.Stdin = r
	cmd.Env = p.pgEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("psql: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
