package forgejo

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

const dumpPath = "/forgejo-dump.zip"

type Connector struct {
	forgejoBin string
	configPath string
	workPath   string
	outputDir  string
	timeout    time.Duration
}

func init() {
	importer.Register("forgejo", location.FLAG_LOCALFS, NewImporter)
	exporter.Register("forgejo", location.FLAG_LOCALFS, NewExporter)
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	conn, err := newConnector(config)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func NewExporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	conn, err := newConnector(config)
	if err != nil {
		return nil, err
	}
	if conn.outputDir == "" {
		conn.outputDir = "."
	}
	return conn, nil
}

func newConnector(config map[string]string) (*Connector, error) {
	timeout := 30 * time.Minute
	if raw := config["timeout"]; raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout: %w", err)
		}
		timeout = parsed
	}

	return &Connector{
		forgejoBin: valueOrDefault(config["forgejo_bin"], "forgejo"),
		configPath: config["config"],
		workPath:   config["work_path"],
		outputDir:  config["output_dir"],
		timeout:    timeout,
	}, nil
}

func valueOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (c *Connector) Origin() string        { return "localhost" }
func (c *Connector) Root() string          { return "/" }
func (c *Connector) Type() string          { return "forgejo" }
func (c *Connector) Flags() location.Flags { return location.FLAG_LOCALFS | location.FLAG_NEEDACK }

func (c *Connector) Ping(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, c.forgejoBin, "--version")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("running %s --version: %w: %s", c.forgejoBin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *Connector) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	tmpDir, err := os.MkdirTemp("", "plakar-forgejo-*")
	if err != nil {
		return err
	}

	reader := func() (io.ReadCloser, error) {
		return c.runDump(ctx, tmpDir)
	}

	info := objects.FileInfo{
		Lname:    path.Base(dumpPath),
		Lsize:    -1,
		Lmode:    0o444,
		LmodTime: time.Now().UTC(),
	}

	records <- connectors.NewRecord(dumpPath, "", info, nil, reader)
	res := <-results
	_ = os.RemoveAll(tmpDir)
	return res.Err
}

func (c *Connector) runDump(ctx context.Context, tmpDir string) (io.ReadCloser, error) {
	dumpCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := []string{"dump", "--type", "zip", "--file", tmpDir}
	if c.configPath != "" {
		args = append(args, "--config", c.configPath)
	}
	if c.workPath != "" {
		args = append(args, "--work-path", c.workPath)
	}

	cmd := exec.CommandContext(dumpCtx, c.forgejoBin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("running forgejo dump: %w: %s", err, strings.TrimSpace(string(out)))
	}

	matches, err := filepath.Glob(filepath.Join(tmpDir, "*.zip"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("forgejo dump did not create a zip archive in %s", tmpDir)
	}

	return os.Open(matches[0])
}

func (c *Connector) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	if err := os.MkdirAll(c.outputDir, 0o755); err != nil {
		return err
	}

	for record := range records {
		if record.Err != nil {
			results <- record.Ok()
			continue
		}
		if record.FileInfo.Lmode.IsDir() {
			results <- record.Ok()
			continue
		}
		if filepath.Base(record.Pathname) != filepath.Base(dumpPath) {
			results <- record.Error(fmt.Errorf("unexpected Forgejo backup file: %s", record.Pathname))
			continue
		}
		if err := c.writeDump(record); err != nil {
			results <- record.Error(err)
			continue
		}
		results <- record.Ok()
	}

	return nil
}

func (c *Connector) writeDump(record *connectors.Record) error {
	if record.Reader == nil {
		return fmt.Errorf("record %s has no reader", record.Pathname)
	}

	target := filepath.Join(c.outputDir, filepath.Base(dumpPath))
	tmp := target + ".tmp"

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, record.Reader); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, target)
}

func (c *Connector) Close(ctx context.Context) error { return nil }
