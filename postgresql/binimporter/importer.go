package binimporter

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path"
	"strings"

	"github.com/PlakarKorp/integration-postgresql/manifest"
	"github.com/PlakarKorp/integration-postgresql/pgconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

func init() {
	importer.Register("postgresql+bin", 0, NewBinImporter)
}

type BinImporter struct {
	conn         pgconn.ConnConfig
	pgBaseBackup string
	psqlBin      string
}

func NewBinImporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	conn, dbPath, err := pgconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}
	if dbPath != "" {
		return nil, fmt.Errorf("postgres+bin: subpath %q is not allowed (pg_basebackup backs up the entire cluster)", dbPath)
	}

	imp := &BinImporter{
		conn:         conn,
		pgBaseBackup: "pg_basebackup",
		psqlBin:      "psql",
	}

	if v, ok := config["pg_basebackup"]; ok && v != "" {
		imp.pgBaseBackup = v
	}
	if v, ok := config["psql"]; ok && v != "" {
		imp.psqlBin = v
	}

	return imp, nil
}

func (p *BinImporter) emitManifest(ctx context.Context, records chan<- *connectors.Record) error {
	sv, svNum, err := manifest.ServerVersion(ctx, p.psqlBin, p.conn, "postgres")
	if err != nil {
		return err
	}

	m := &manifest.Manifest{
		Connector:        "postgresql+bin",
		Host:             p.conn.Host,
		Port:             p.conn.Port,
		ServerVersion:    sv,
		ServerVersionNum: svNum,
		DumpFormat:       "basebackup",
	}

	// Collect cluster-level metadata.  Failures are non-fatal.
	m.ClusterSystemIdentifier = manifest.QueryClusterSystemID(ctx, p.psqlBin, p.conn, "postgres")
	m.InRecovery = manifest.QueryInRecovery(ctx, p.psqlBin, p.conn, "postgres")
	if cfg, err := manifest.QueryClusterConfig(ctx, p.psqlBin, p.conn, "postgres"); err == nil {
		m.ClusterConfig = &cfg
	}
	if roles, err := manifest.QueryRoles(ctx, p.psqlBin, p.conn, "postgres"); err == nil {
		m.Roles = roles
	}
	if tss, err := manifest.QueryTablespaces(ctx, p.psqlBin, p.conn, "postgres"); err == nil {
		m.Tablespaces = tss
	}
	// Include the basic database list (no per-database detail: the physical
	// backup already captures every database at the file level).
	if dbs, err := manifest.QueryDatabases(ctx, p.psqlBin, p.conn, "postgres"); err == nil {
		m.Databases = dbs
	}

	return manifest.EmitManifest(ctx, records, m)
}

// Import runs pg_basebackup in tar-to-stdout mode and emits one record per
// tar entry.  The results channel is used to pace reading: the loop waits for
// an ack after each record so the caller fully consumes the entry before
// tr.Next() advances the stream.
func (p *BinImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	if err := p.emitManifest(ctx, records); err != nil {
		return err
	}

	<-results // wait for the manifest ack

	args := append(p.conn.Args(), "-D", "-", "-F", "tar", "-X", "fetch")

	cmd := exec.CommandContext(ctx, p.pgBaseBackup, args...)
	cmd.Stdin = nil
	cmd.Env = p.conn.Env()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pg_basebackup: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pg_basebackup: %w", err)
	}

	tr := tar.NewReader(stdout)

	for {
		hdr, err := tr.Next()
		if err != nil {
			_ = cmd.Process.Kill()
			waitErr := cmd.Wait()
			if !errors.Is(err, io.EOF) {
				if s := strings.TrimSpace(stderr.String()); s != "" {
					return fmt.Errorf("reading backup stream: %w: %s", err, s)
				}
				return fmt.Errorf("reading backup stream: %w", err)
			}
			if waitErr != nil {
				return fmt.Errorf("pg_basebackup: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
			}
			return nil
		}

		name := path.Join("/data/" + hdr.Name)
		records <- &connectors.Record{
			Pathname: name,
			Target:   hdr.Linkname,
			FileInfo: finfo(hdr),
			Reader:   io.NopCloser(tr),
		}

		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return ctx.Err()
		case <-results:
			// wait for the ack before continuing
		}
	}
}

// finfo has been copied from integration-tar.
func finfo(hdr *tar.Header) objects.FileInfo {
	f := objects.FileInfo{
		Lname:      path.Base(hdr.Name),
		Lsize:      hdr.Size,
		Lmode:      fs.FileMode(hdr.Mode),
		LmodTime:   hdr.ModTime,
		Ldev:       0, // XXX could use hdr.Devminor / hdr.Devmajor
		Luid:       uint64(hdr.Uid),
		Lgid:       uint64(hdr.Gid),
		Lnlink:     1,
		Lusername:  hdr.Uname,
		Lgroupname: hdr.Gname,
	}

	switch hdr.Typeflag {
	case tar.TypeLink:
		f.Lmode |= fs.ModeSymlink
	case tar.TypeChar:
		f.Lmode |= fs.ModeCharDevice
	case tar.TypeBlock:
		f.Lmode |= fs.ModeDevice
	case tar.TypeDir:
		f.Lmode |= fs.ModeDir
	case tar.TypeFifo:
		f.Lmode |= fs.ModeNamedPipe
	default:
		// other are implicitly regular files.
	}

	return f
}

// Ping verifies that the PostgreSQL server is reachable.
func (p *BinImporter) Ping(ctx context.Context) error {
	return p.conn.Ping(ctx, p.psqlBin, "")
}

func (p *BinImporter) Close(ctx context.Context) error { return nil }
func (p *BinImporter) Root() string                    { return "/" }
func (p *BinImporter) Origin() string                  { return p.conn.Host }
func (p *BinImporter) Type() string                    { return "postgres+bin" }

func (p *BinImporter) Flags() location.Flags {
	// FLAG_STREAM: the tar stream is non-seekable and must be read sequentially.
	// FLAG_NEEDACK: each entry must be fully consumed before tr.Next() can advance;
	//               the ack signals that the caller is done with the current entry.
	return location.FLAG_STREAM | location.FLAG_NEEDACK
}
