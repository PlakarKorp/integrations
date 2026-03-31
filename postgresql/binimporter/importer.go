package binimporter

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

func init() {
	importer.Register("postgres+bin", 0, NewBinImporter)
}

type BinImporter struct {
	host         string
	port         string
	username     string
	password     string
	pgBaseBackup string
}

func NewBinImporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	imp := &BinImporter{
		host:         "localhost",
		port:         "5432",
		pgBaseBackup: "pg_basebackup",
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
		if u.Path != "" && u.Path != "/" {
			return nil, fmt.Errorf("postgres+bin: subpath %q is not allowed (pg_basebackup backs up the entire cluster)", u.Path)
		}
		if u.User != nil {
			if u.User.Username() != "" {
				imp.username = u.User.Username()
			}
			if p, ok := u.User.Password(); ok {
				imp.password = p
			}
		}
	}

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
	if v, ok := config["pg_basebackup"]; ok && v != "" {
		imp.pgBaseBackup = v
	}

	return imp, nil
}

func (p *BinImporter) pgEnv() []string {
	env := os.Environ()
	if p.password != "" {
		env = append(env, "PGPASSWORD="+p.password)
	}
	return env
}

// Import runs pg_basebackup in tar-to-stdout mode and emits one record per
// tar entry.  The results channel is used to pace reading: the loop waits for
// an ack after each record so the caller fully consumes the entry before
// tr.Next() advances the stream.
func (p *BinImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	args := []string{
		"-h", p.host, "-p", p.port, "-w",
		"-D", "-", "-F", "tar", "-X", "fetch",
	}
	if p.username != "" {
		args = append(args, "-U", p.username)
	}

	cmd := exec.CommandContext(ctx, p.pgBaseBackup, args...)
	cmd.Stdin = nil
	cmd.Env = p.pgEnv()

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

		name := path.Join("/" + hdr.Name)
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
	args := []string{"-h", p.host, "-p", p.port, "-d", "postgres", "-w", "-c", "SELECT 1", "-q", "--no-psqlrc"}
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

func (p *BinImporter) Close(ctx context.Context) error { return nil }
func (p *BinImporter) Root() string                    { return "/" }
func (p *BinImporter) Origin() string                  { return p.host }
func (p *BinImporter) Type() string                    { return "postgres+bin" }

func (p *BinImporter) Flags() location.Flags {
	// FLAG_STREAM: the tar stream is non-seekable and must be read sequentially.
	// FLAG_NEEDACK: each entry must be fully consumed before tr.Next() can advance;
	//               the ack signals that the caller is done with the current entry.
	return location.FLAG_STREAM | location.FLAG_NEEDACK
}
