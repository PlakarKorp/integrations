package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-mysql/manifest"
	"github.com/PlakarKorp/integration-mysql/mysqlconn"
	"github.com/PlakarKorp/kloset/connectors"
	iimporter "github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

// Importer streams a logical MySQL backup using mysqldump.
type Importer struct {
	conn              mysqlconn.ConnConfig
	database          string // empty = --all-databases
	noData            bool
	noCreateInfo      bool
	noTablespaces     bool
	columnStatistics  bool
	singleTransaction bool
	routines          bool
	events            bool
	triggers          bool
	hexBlob           bool
	setGTIDPurged     string
}

// New constructs an Importer from the connector configuration map.
func New(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (iimporter.Importer, error) {
	conn, err := mysqlconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}

	boolOpt := func(key string, def bool) (bool, error) {
		v, ok := config[key]
		if !ok || v == "" {
			return def, nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("invalid value for %s: %w", key, err)
		}
		return b, nil
	}

	imp := &Importer{conn: conn, database: mysqlconn.DatabaseFromConfig(config), setGTIDPurged: config["set_gtid_purged"]}
	if imp.singleTransaction, err = boolOpt("single_transaction", true); err != nil {
		return nil, err
	}
	if imp.routines, err = boolOpt("routines", true); err != nil {
		return nil, err
	}
	if imp.events, err = boolOpt("events", true); err != nil {
		return nil, err
	}
	if imp.triggers, err = boolOpt("triggers", true); err != nil {
		return nil, err
	}
	if imp.noData, err = boolOpt("no_data", false); err != nil {
		return nil, err
	}
	if imp.noCreateInfo, err = boolOpt("no_create_info", false); err != nil {
		return nil, err
	}
	if imp.hexBlob, err = boolOpt("hex_blob", false); err != nil {
		return nil, err
	}
	if imp.noTablespaces, err = boolOpt("no_tablespaces", true); err != nil {
		return nil, err
	}
	if imp.columnStatistics, err = boolOpt("column_statistics", true); err != nil {
		return nil, err
	}

	if imp.noData && imp.noCreateInfo {
		return nil, fmt.Errorf("no_data and no_create_info are mutually exclusive")
	}

	return imp, nil
}

// Origin returns a human-readable source identifier.
func (i *Importer) Origin() string {
	if i.database != "" {
		return "mysql://" + i.conn.Host + ":" + i.conn.Port + "/" + i.database
	}
	return "mysql://" + i.conn.Host + ":" + i.conn.Port
}

// Type returns the connector type label.
func (i *Importer) Type() string { return "mysql" }

// Root returns the root path of the backup.
func (i *Importer) Root() string { return "/" }

// Flags returns location.FLAG_STREAM: the importer produces a single-pass stream.
func (i *Importer) Flags() location.Flags { return location.FLAG_STREAM }

// Ping verifies connectivity to the MySQL server.
func (i *Importer) Ping(ctx context.Context) error {
	return i.conn.Ping(ctx)
}

// Close is a no-op for this importer.
func (i *Importer) Close(_ context.Context) error { return nil }

// Import emits the manifest and then the mysqldump output as records.
// The results channel is not used (FLAG_STREAM, no FLAG_NEEDACK).
func (i *Importer) Import(ctx context.Context, records chan<- *connectors.Record, _ <-chan *connectors.Result) error {
	defer close(records)

	// Step 1: emit /manifest.json.
	manifestCfg := manifest.Config{
		Conn:     i.conn,
		Database: i.database,
		Options: manifest.ManifestOptions{
			NoData:            i.noData,
			NoCreateInfo:      i.noCreateInfo,
			NoTablespaces:     i.noTablespaces,
			ColumnStatistics:  i.columnStatistics,
			SingleTransaction: i.singleTransaction,
			Routines:          i.routines,
			Events:            i.events,
			Triggers:          i.triggers,
			HexBlob:           i.hexBlob,
			SetGTIDPurged:     i.setGTIDPurged,
		},
	}
	if err := manifest.Emit(ctx, manifestCfg, records); err != nil {
		return fmt.Errorf("emitting manifest: %w", err)
	}

	// Step 2: emit the dump file.
	if i.database != "" {
		return i.dumpSingleDatabase(ctx, records)
	}
	return i.dumpAllDatabases(ctx, records)
}

// dumpSingleDatabase runs mysqldump for one database and emits /<database>.sql.
func (i *Importer) dumpSingleDatabase(ctx context.Context, records chan<- *connectors.Record) error {
	pathname := "/" + i.database + ".sql"
	return i.emitDump(ctx, records, pathname, func() (io.ReadCloser, error) {
		args := i.conn.Args()
		args = append(args, i.dumpFlags()...)
		args = append(args, i.database)
		return i.startDump(ctx, args)
	})
}

// dumpAllDatabases runs mysqldump --all-databases and emits /all.sql.
func (i *Importer) dumpAllDatabases(ctx context.Context, records chan<- *connectors.Record) error {
	return i.emitDump(ctx, records, "/all.sql", func() (io.ReadCloser, error) {
		args := i.conn.Args()
		args = append(args, "--all-databases")
		args = append(args, i.dumpFlags()...)
		return i.startDump(ctx, args)
	})
}

// dumpFlags returns mysqldump flags derived from the importer options.
func (i *Importer) dumpFlags() []string {
	var flags []string
	if i.singleTransaction {
		flags = append(flags, "--single-transaction")
	}
	if i.routines {
		flags = append(flags, "--routines")
	}
	if i.events {
		flags = append(flags, "--events")
	}
	if !i.triggers {
		flags = append(flags, "--skip-triggers")
	}
	if i.noData {
		flags = append(flags, "--no-data")
	}
	if i.noCreateInfo {
		flags = append(flags, "--no-create-info")
	}
	if i.noTablespaces {
		flags = append(flags, "--no-tablespaces")
	}
	if !i.columnStatistics {
		flags = append(flags, "--column-statistics=0")
	}
	if i.hexBlob {
		flags = append(flags, "--hex-blob")
	}
	if i.setGTIDPurged != "" {
		flags = append(flags, "--set-gtid-purged="+i.setGTIDPurged)
	}
	return flags
}

// cmdReader wraps a command's stdout and captures its exit status on Close.
type cmdReader struct {
	io.ReadCloser // stdout pipe
	cmd           *exec.Cmd
	stderr        *bytes.Buffer
}

func (r *cmdReader) Close() error {
	// Close stdout so the command sees EOF and can exit cleanly.
	r.ReadCloser.Close()
	if err := r.cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(r.stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// startDump starts mysqldump with the given args and returns a ReadCloser
// that streams stdout.  The exit status is captured on Close.
func (i *Importer) startDump(ctx context.Context, args []string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, i.conn.BinPath("mysqldump"), args...)
	cmd.Env = i.conn.Env()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting mysqldump: %w", err)
	}
	return &cmdReader{
		ReadCloser: stdout,
		cmd:        cmd,
		stderr:     &stderr,
	}, nil
}

// emitDump sends a single dump record on the records channel.
func (i *Importer) emitDump(ctx context.Context, records chan<- *connectors.Record, pathname string, readerFunc func() (io.ReadCloser, error)) error {
	now := time.Now().UTC()
	fileinfo := objects.FileInfo{
		Lname:    path.Base(pathname),
		Lsize:    0, // unknown until streamed
		Lmode:    0444,
		LmodTime: now,
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- connectors.NewRecord(pathname, "", fileinfo, nil, readerFunc):
	}
	return nil
}
