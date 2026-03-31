package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-postgresql/manifest"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

func init() {
	importer.Register("postgresql", 0, NewImporter)
}

type Importer struct {
	host       string
	port       string
	username   string
	password   string
	database   string // empty means back up all databases via pg_dumpall
	compress   bool   // enable pg_dump compression; off by default to avoid degrading Plakar's compression
	schemaOnly bool   // pass -s: dump schema only
	dataOnly   bool   // pass -a: dump data only
	pgDump     string
	pgDumpAll  string
}

func NewImporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	imp := &Importer{
		host:      "localhost",
		port:      "5432",
		pgDump:    "pg_dump",
		pgDumpAll: "pg_dumpall",
	}

	if loc, ok := config["location"]; ok && loc != "" {
		u, err := url.Parse(loc)
		if err != nil {
			return nil, fmt.Errorf("invalid location: %w", err)
		}
		if u.Hostname() != "" {
			imp.host = u.Hostname()
		}
		if u.Port() != "" {
			imp.port = u.Port()
		}
		if u.User != nil {
			if u.User.Username() != "" {
				imp.username = u.User.Username()
			}
			if p, ok := u.User.Password(); ok {
				imp.password = p
			}
		}
		if u.Path != "" && u.Path != "/" {
			imp.database = strings.TrimPrefix(u.Path, "/")
		}
	}

	// Standalone fields override URI components.
	if h, ok := config["host"]; ok && h != "" {
		imp.host = h
	}
	if p, ok := config["port"]; ok && p != "" {
		imp.port = p
	}
	if u, ok := config["username"]; ok && u != "" {
		imp.username = u
	}
	if p, ok := config["password"]; ok && p != "" {
		imp.password = p
	}
	if db, ok := config["database"]; ok && db != "" {
		imp.database = db
	}
	if v, ok := config["pg_dump"]; ok && v != "" {
		imp.pgDump = v
	}
	if v, ok := config["pg_dumpall"]; ok && v != "" {
		imp.pgDumpAll = v
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

func (p *Importer) pgEnv() []string {
	env := os.Environ()
	if p.password != "" {
		env = append(env, "PGPASSWORD="+p.password)
	}
	return env
}

func (p *Importer) emitManifest(ctx context.Context, records chan<- *connectors.Record, dumpFormat string) error {
	connectDB := p.database
	if connectDB == "" {
		connectDB = "postgres"
	}
	sv, svNum, err := manifest.ServerVersion(ctx, "psql", p.host, p.port, connectDB, p.username, p.pgEnv())
	if err != nil {
		return err
	}
	return manifest.EmitManifest(ctx, records, &manifest.Manifest{
		Connector:        "postgresql",
		Host:             p.host,
		Port:             p.port,
		ServerVersion:    sv,
		ServerVersionNum: svNum,
		Database:         p.database,
		DumpFormat:       dumpFormat,
		Options: &manifest.ManifestOptions{
			SchemaOnly: p.schemaOnly,
			DataOnly:   p.dataOnly,
			Compress:   p.compress,
		},
	})
}

func (p *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	if p.database != "" {
		if err := p.emitManifest(ctx, records, "custom"); err != nil {
			return err
		}
		return p.dumpDatabase(ctx, records, p.database)
	}
	if err := p.emitManifest(ctx, records, "sql"); err != nil {
		return err
	}
	return p.dumpAll(ctx, records)
}

// dumpDatabase runs pg_dump -Fc and emits one record named /<dbname>.dump.
func (p *Importer) dumpDatabase(ctx context.Context, records chan<- *connectors.Record, dbname string) error {
	args := []string{"-h", p.host, "-p", p.port, "-w", "-Fc"}
	if !p.compress {
		args = append(args, "-Z0")
	}
	if p.schemaOnly {
		args = append(args, "-s")
	} else if p.dataOnly {
		args = append(args, "-a")
	}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}
	args = append(args, dbname)

	return p.emitRecord(ctx, records, p.pgDump, args, "/"+dbname+".dump")
}

// dumpAll runs pg_dumpall and emits one record named /all.sql.
func (p *Importer) dumpAll(ctx context.Context, records chan<- *connectors.Record) error {
	args := []string{"-h", p.host, "-p", p.port, "-w"}
	if p.schemaOnly {
		args = append(args, "-s")
	} else if p.dataOnly {
		args = append(args, "-a")
	}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}

	return p.emitRecord(ctx, records, p.pgDumpAll, args, "/all.sql")
}

// emitRecord starts bin with args and sends a streaming Record on records.
// The record size is 0 because the dump size is not known until the stream is consumed.
func (p *Importer) emitRecord(ctx context.Context, records chan<- *connectors.Record, bin string, args []string, recordPath string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	cmd.Env = p.pgEnv()

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
		Lname:    recordPath,
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
	connectDB := p.database
	if connectDB == "" {
		connectDB = "postgres"
	}
	args := []string{"-h", p.host, "-p", p.port, "-d", connectDB, "-w", "-c", "SELECT 1", "-q", "--no-psqlrc"}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}
	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Stdin = nil
	cmd.Env = p.pgEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ping: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (p *Importer) Close(ctx context.Context) error { return nil }
func (p *Importer) Root() string                    { return "/" }
func (p *Importer) Origin() string                  { return p.host }
func (p *Importer) Type() string                    { return "postgresql" }

// Flags returns FLAG_STREAM to signal that Import produces a single streaming
// pass. Without it, the framework calls Import twice — once to enumerate
// records and once to read their contents — which would cause the manifest
// and the dump to be emitted twice.
func (p *Importer) Flags() location.Flags {
	return location.FLAG_STREAM
}
