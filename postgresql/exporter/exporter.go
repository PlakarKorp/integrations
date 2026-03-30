package exporter

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	exporter.Register("postgresql", 0, NewExporter)
}

type Exporter struct {
	host     string
	port     string
	username string
	password string
	database string // target database for pg_restore (single-db restores)
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	exp := &Exporter{
		host: "localhost",
		port: "5432",
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
	return exp, nil
}

// pgEnv returns the process environment augmented with PGPASSWORD so that
// pg_restore / psql can authenticate without an interactive prompt.
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

// Ping runs "SELECT 1" against the server to verify connectivity.
func (p *Exporter) Ping(ctx context.Context) error {
	connectDB := p.database
	if connectDB == "" {
		connectDB = "postgres"
	}
	args := []string{"-h", p.host, "-p", p.port, "-d", connectDB, "-w", "-c", "SELECT 1", "-q", "--no-psqlrc"}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}
	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Stdin = nil // never read from stdin; -w prevents password prompts
	cmd.Env = p.pgEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ping: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (p *Exporter) Close(ctx context.Context) error {
	return nil
}

// Export iterates over the records channel and restores each dump record into
// the configured PostgreSQL server.  Records ending in ".dump" are restored
// via pg_restore (custom format); records ending in ".sql" are fed to psql
// (plain-SQL format produced by pg_dumpall).  Directory and xattr records are
// acknowledged without action.
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

// restore dispatches a record's reader directly to the appropriate restore
// tool based on the record's file extension.
func (p *Exporter) restore(ctx context.Context, record *connectors.Record) error {
	if strings.HasSuffix(record.Pathname, ".dump") {
		return p.pgRestore(ctx, record.Reader, record.Pathname)
	}
	return p.psqlRestore(ctx, record.Reader)
}

// pgRestore restores a custom-format dump produced by pg_dump -Fc.
// If a target database is configured, it is created when it does not exist
// yet and the dump is restored into it.  Otherwise the database name is
// inferred from the dump filename (e.g. "/mydb.dump" → "mydb").
// The dump is streamed directly from r into pg_restore's stdin.
func (p *Exporter) pgRestore(ctx context.Context, r io.Reader, pathname string) error {
	targetDB := p.database
	if targetDB == "" {
		targetDB = strings.TrimSuffix(filepath.Base(pathname), ".dump")
	}
	if err := p.ensureDatabase(ctx, targetDB); err != nil {
		return err
	}

	args := []string{"-h", p.host, "-p", p.port, "-w", "-d", targetDB}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}

	cmd := exec.CommandContext(ctx, "pg_restore", args...)
	cmd.Stdin = r
	cmd.Env = p.pgEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_restore: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ensureDatabase creates dbname if it does not already exist.
// It uses \gexec so the query either executes CREATE DATABASE (when the
// database is absent) or returns no rows and does nothing — no error
// in either case.
func (p *Exporter) ensureDatabase(ctx context.Context, dbname string) error {
	// Escape the identifier and the literal separately.
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
	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Stdin = strings.NewReader(query)
	cmd.Env = p.pgEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create database %q: %w: %s", dbname, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// psqlRestore feeds a plain-SQL dump (produced by pg_dumpall) to psql,
// connecting to the "postgres" maintenance database so that the script can
// CREATE and configure other databases freely.
// The dump is streamed directly from r into psql's stdin.
func (p *Exporter) psqlRestore(ctx context.Context, r io.Reader) error {
	args := []string{"-h", p.host, "-p", p.port, "-w", "-d", "postgres"}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}

	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Stdin = r
	cmd.Env = p.pgEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("psql: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
