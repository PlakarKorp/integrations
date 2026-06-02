package forgejo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	iimporter "github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

func init() {
	iimporter.Register("forgejo", 0, NewImporter)
}

type Importer struct {
	cfg config
}

func NewImporter(_ context.Context, _ *connectors.Options, _ string, values map[string]string) (iimporter.Importer, error) {
	cfg, err := parseImporterConfig(values)
	if err != nil {
		return nil, err
	}
	return &Importer{cfg: cfg}, nil
}

func (i *Importer) Origin() string        { return i.cfg.location }
func (i *Importer) Type() string          { return "forgejo" }
func (i *Importer) Root() string          { return "/" }
func (i *Importer) Flags() location.Flags { return location.FLAG_STREAM }
func (i *Importer) Close(_ context.Context) error {
	return nil
}

func (i *Importer) Ping(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, i.cfg.forgejoBin, "--version")
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func (i *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	name := archiveName(i.cfg.dumpType)
	fileinfo := objects.FileInfo{
		Lname:    name,
		Lsize:    -1,
		Lmode:    0444,
		LmodTime: time.Now().UTC(),
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- connectors.NewRecord("/"+name, "", fileinfo, nil, func() (io.ReadCloser, error) {
		return i.startDump(ctx)
	}):
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-results:
		return result.Err
	}
}

func (i *Importer) dumpArgs() []string {
	args := []string{
		"dump",
		"--file", "-",
		"--type", i.cfg.dumpType,
		"--quiet",
	}
	if i.cfg.workPath != "" {
		args = append(args, "--work-path", i.cfg.workPath)
	}
	if i.cfg.customPath != "" {
		args = append(args, "--custom-path", i.cfg.customPath)
	}
	if i.cfg.configPath != "" {
		args = append(args, "--config", i.cfg.configPath)
	}
	if i.cfg.tempDir != "" {
		args = append(args, "--tempdir", i.cfg.tempDir)
	}
	if i.cfg.database != "" {
		args = append(args, "--database", i.cfg.database)
	}
	if i.cfg.skipRepository {
		args = append(args, "--skip-repository")
	}
	if i.cfg.skipLog {
		args = append(args, "--skip-log")
	}
	if i.cfg.skipCustomDir {
		args = append(args, "--skip-custom-dir")
	}
	if i.cfg.skipLFSData {
		args = append(args, "--skip-lfs-data")
	}
	if i.cfg.skipAttachmentData {
		args = append(args, "--skip-attachment-data")
	}
	if i.cfg.skipPackageData {
		args = append(args, "--skip-package-data")
	}
	if i.cfg.skipIndex {
		args = append(args, "--skip-index")
	}
	if i.cfg.skipRepoArchives {
		args = append(args, "--skip-repo-archives")
	}
	return args
}

type commandReadCloser struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func (r *commandReadCloser) Close() error {
	_ = r.ReadCloser.Close()
	err := r.cmd.Wait()
	if err != nil {
		if msg := strings.TrimSpace(r.stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func (i *Importer) startDump(ctx context.Context) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, i.cfg.forgejoBin, i.dumpArgs()...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating forgejo dump stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting forgejo dump: %w", err)
	}
	return &commandReadCloser{ReadCloser: stdout, cmd: cmd, stderr: &stderr}, nil
}
