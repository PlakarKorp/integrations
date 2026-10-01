package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-redis/redisconn"
	"github.com/PlakarKorp/kloset/connectors"
	iimporter "github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

const DumpPath = "/dump.rdb"

type Importer struct {
	proto         string
	conn          redisconn.ConnConfig
	output        string
	triggerBGSAVE bool
	waitTimeout   time.Duration
}

func New(proto string, conn redisconn.ConnConfig, config map[string]string) (*Importer, error) {
	imp := &Importer{proto: proto, conn: conn, output: DumpPath, triggerBGSAVE: true, waitTimeout: 5 * time.Minute}
	if v := config["output"]; v != "" {
		if !strings.HasPrefix(v, "/") {
			v = "/" + v
		}
		imp.output = path.Clean(v)
	}
	if v := config["trigger_bgsave"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid value for trigger_bgsave: %w", err)
		}
		imp.triggerBGSAVE = b
	}
	if v := config["wait_timeout"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid value for wait_timeout: %w", err)
		}
		imp.waitTimeout = d
	}
	return imp, nil
}

func (i *Importer) Origin() string                 { return i.conn.Origin(i.proto) }
func (i *Importer) Type() string                   { return i.proto }
func (i *Importer) Root() string                   { return "/" }
func (i *Importer) Flags() location.Flags          { return location.FLAG_STREAM }
func (i *Importer) Ping(ctx context.Context) error { return i.conn.Ping(ctx) }
func (i *Importer) Close(_ context.Context) error  { return nil }

var _ iimporter.Importer = (*Importer)(nil)

func (i *Importer) Import(ctx context.Context, records chan<- *connectors.Record, _ <-chan *connectors.Result) error {
	defer close(records)
	if i.triggerBGSAVE {
		if err := i.run(ctx, "BGSAVE"); err != nil {
			if !strings.Contains(err.Error(), "Background save already in progress") {
				return err
			}
		}
		if err := i.waitForBGSAVE(ctx); err != nil {
			return err
		}
	}
	reader, err := i.dumpReader(ctx)
	if err != nil {
		return err
	}
	fileinfo := objects.FileInfo{Lname: path.Base(i.output), Lmode: 0444, LmodTime: time.Now().UTC()}
	select {
	case <-ctx.Done():
		_ = reader.Close()
		return ctx.Err()
	case records <- connectors.NewRecord(i.output, "", fileinfo, nil, func() (io.ReadCloser, error) { return reader, nil }):
		return nil
	}
}

func (i *Importer) dumpReader(ctx context.Context) (io.ReadCloser, error) {
	if src, err := i.rdbPath(ctx); err == nil && src != "" {
		return os.Open(src)
	}
	return i.redisCLIDump(ctx)
}

func (i *Importer) rdbPath(ctx context.Context) (string, error) {
	dir, err := i.configGet(ctx, "dir")
	if err != nil {
		return "", err
	}
	dbfilename, err := i.configGet(ctx, "dbfilename")
	if err != nil {
		return "", err
	}
	if dir == "" || dbfilename == "" {
		return "", fmt.Errorf("Redis did not return dir/dbfilename")
	}
	return filepath.Join(dir, dbfilename), nil
}

func (i *Importer) configGet(ctx context.Context, key string) (string, error) {
	out, err := i.outputCommand(ctx, "CONFIG", "GET", key)
	if err != nil {
		return "", err
	}
	lines := nonEmptyLines(out)
	if len(lines) < 2 {
		return "", fmt.Errorf("unexpected CONFIG GET %s response", key)
	}
	return lines[len(lines)-1], nil
}

func (i *Importer) waitForBGSAVE(ctx context.Context) error {
	deadline := time.Now().Add(i.waitTimeout)
	for {
		out, err := i.outputCommand(ctx, "INFO", "persistence")
		if err != nil {
			return err
		}
		if strings.Contains(out, "rdb_bgsave_in_progress:0") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for Redis BGSAVE")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (i *Importer) run(ctx context.Context, args ...string) error {
	_, err := i.outputCommand(ctx, args...)
	return err
}

func (i *Importer) outputCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, i.conn.Bin(), i.conn.Args(args...)...)
	cmd.Env = i.conn.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("redis-cli %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (i *Importer) redisCLIDump(ctx context.Context) (io.ReadCloser, error) {
	tmp, err := os.CreateTemp("", "plakar-redis-*.rdb")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()

	cmd := exec.CommandContext(ctx, i.conn.Bin(), i.conn.Args("--rdb", tmpName)...)
	cmd.Env = i.conn.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("redis-cli --rdb failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fp, err := os.Open(tmpName)
	if err != nil {
		_ = os.Remove(tmpName)
		return nil, err
	}
	return &tempFileReader{File: fp, name: tmpName}, nil
}

type cmdReader struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func (r *cmdReader) Close() error {
	_ = r.ReadCloser.Close()
	err := r.cmd.Wait()
	if err != nil && strings.TrimSpace(r.stderr.String()) != "" {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(r.stderr.String()))
	}
	return err
}

type tempFileReader struct {
	*os.File
	name string
}

func (r *tempFileReader) Close() error {
	err := r.File.Close()
	if rmErr := os.Remove(r.name); err == nil {
		err = rmErr
	}
	return err
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
