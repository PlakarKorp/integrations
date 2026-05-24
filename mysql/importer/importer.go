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

// AllDatabasesDumpFile is the snapshot path used for full-server dumps.
const AllDatabasesDumpFile = "/all.sql"

// Importer holds the parameters common to all MySQL-compatible backup connectors.
// Provider-specific options (e.g. column_statistics for MySQL) are handled by
// structs in the plugin packages that embed this type.
type Importer struct {
	Proto             string
	Conn              mysqlconn.ConnConfig
	Database          string
	NoData            bool
	NoCreateInfo      bool
	NoTablespaces     bool
	SingleTransaction bool
	Routines          bool
	Events            bool
	Triggers          bool
	HexBlob           bool
}

// New parses the common importer options from config and returns a base Importer.
// The caller is responsible for setting Conn.ClientBin and Conn.DumpBin before use.
func New(proto string, conn mysqlconn.ConnConfig, config map[string]string) (*Importer, error) {
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

	imp := &Importer{
		Proto:    proto,
		Conn:     conn,
		Database: mysqlconn.DatabaseFromConfig(config),
	}
	var err error
	if imp.SingleTransaction, err = boolOpt("single_transaction", true); err != nil {
		return nil, err
	}
	if imp.Routines, err = boolOpt("routines", true); err != nil {
		return nil, err
	}
	if imp.Events, err = boolOpt("events", true); err != nil {
		return nil, err
	}
	if imp.Triggers, err = boolOpt("triggers", true); err != nil {
		return nil, err
	}
	if imp.NoData, err = boolOpt("no_data", false); err != nil {
		return nil, err
	}
	if imp.NoCreateInfo, err = boolOpt("no_create_info", false); err != nil {
		return nil, err
	}
	if imp.HexBlob, err = boolOpt("hex_blob", false); err != nil {
		return nil, err
	}
	if imp.NoTablespaces, err = boolOpt("no_tablespaces", true); err != nil {
		return nil, err
	}
	if imp.NoData && imp.NoCreateInfo {
		return nil, fmt.Errorf("no_data and no_create_info are mutually exclusive")
	}
	return imp, nil
}

func (i *Importer) Origin() string {
	if i.Database != "" {
		return i.Proto + "://" + i.Conn.Host + ":" + i.Conn.Port + "/" + i.Database
	}
	return i.Proto + "://" + i.Conn.Host + ":" + i.Conn.Port
}

func (i *Importer) Type() string                  { return i.Proto }
func (i *Importer) Root() string                  { return "/" }
func (i *Importer) Flags() location.Flags         { return location.FLAG_STREAM }
func (i *Importer) Ping(ctx context.Context) error { return i.Conn.Ping(ctx) }
func (i *Importer) Close(_ context.Context) error  { return nil }

// CommonManifestOptions returns the shared ManifestOptions.
// The caller should set any provider-specific fields before use.
func (i *Importer) CommonManifestOptions() manifest.ManifestOptions {
	return manifest.ManifestOptions{
		NoData:            i.NoData,
		NoCreateInfo:      i.NoCreateInfo,
		NoTablespaces:     i.NoTablespaces,
		SingleTransaction: i.SingleTransaction,
		Routines:          i.Routines,
		Events:            i.Events,
		Triggers:          i.Triggers,
		HexBlob:           i.HexBlob,
	}
}

// CommonDumpFlags returns the shared dump flags.
// The caller should append any provider-specific flags before invoking the dump tool.
func (i *Importer) CommonDumpFlags() []string {
	var flags []string
	if i.SingleTransaction {
		flags = append(flags, "--single-transaction")
	}
	if i.Routines {
		flags = append(flags, "--routines")
	}
	if i.Events {
		flags = append(flags, "--events")
	}
	if !i.Triggers {
		flags = append(flags, "--skip-triggers")
	}
	if i.NoData {
		flags = append(flags, "--no-data")
	}
	if i.NoCreateInfo {
		flags = append(flags, "--no-create-info")
	}
	if i.NoTablespaces {
		flags = append(flags, "--no-tablespaces")
	}
	if i.HexBlob {
		flags = append(flags, "--hex-blob")
	}
	return flags
}

// Run is the shared entry point called by provider-specific Import() implementations.
// It emits the manifest then the dump; extraFlags are appended to CommonDumpFlags().
func (i *Importer) Run(ctx context.Context, records chan<- *connectors.Record, cfg manifest.Config, extraFlags []string) error {
	defer close(records)

	if err := manifest.Emit(ctx, cfg, records); err != nil {
		return fmt.Errorf("emitting manifest: %w", err)
	}

	flags := append(i.CommonDumpFlags(), extraFlags...)
	if i.Database != "" {
		return i.dumpSingleDatabase(ctx, records, flags)
	}
	return i.dumpAllDatabases(ctx, records, flags)
}

var _ iimporter.Importer = (*Importer)(nil)

// Import must be overridden by each provider plugin.
func (i *Importer) Import(_ context.Context, _ chan<- *connectors.Record, _ <-chan *connectors.Result) error {
	return fmt.Errorf("Import not implemented: provider must override this method")
}

func (i *Importer) dumpSingleDatabase(ctx context.Context, records chan<- *connectors.Record, flags []string) error {
	pathname := "/" + i.Database + ".sql"
	return i.emitDump(ctx, records, pathname, func() (io.ReadCloser, error) {
		extra := append(flags, i.Database)
		return i.startDump(ctx, extra)
	})
}

func (i *Importer) dumpAllDatabases(ctx context.Context, records chan<- *connectors.Record, flags []string) error {
	return i.emitDump(ctx, records, AllDatabasesDumpFile, func() (io.ReadCloser, error) {
		extra := append([]string{"--all-databases"}, flags...)
		return i.startDump(ctx, extra)
	})
}

// cmdReader captures the exit status and stderr of a dump command on Close.
type cmdReader struct {
	io.ReadCloser
	cmd     *exec.Cmd
	stderr  *bytes.Buffer
	cleanup func() // removes the temporary password file, if any
}

func (r *cmdReader) Close() error {
	r.ReadCloser.Close()
	err := r.cmd.Wait()
	r.cleanup()
	if err != nil {
		if msg := strings.TrimSpace(r.stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func (i *Importer) startDump(ctx context.Context, extraArgs []string) (io.ReadCloser, error) {
	pwArg, cleanup, err := i.Conn.PasswordFileArg()
	if err != nil {
		return nil, err
	}
	args := i.Conn.ArgsWithPassword(pwArg)
	args = append(args, extraArgs...)

	cmd := exec.CommandContext(ctx, i.Conn.BinPath(i.Conn.DumpBin), args...)
	cmd.Env = i.Conn.Env()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("starting %s: %w", i.Conn.DumpBin, err)
	}
	return &cmdReader{ReadCloser: stdout, cmd: cmd, stderr: &stderr, cleanup: cleanup}, nil
}

func (i *Importer) emitDump(ctx context.Context, records chan<- *connectors.Record, pathname string, readerFunc func() (io.ReadCloser, error)) error {
	now := time.Now().UTC()
	fileinfo := objects.FileInfo{
		Lname:    path.Base(pathname),
		Lsize:    0,
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
