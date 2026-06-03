package exporter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

const flags = location.FLAG_LOCALFS

func init() {
	exporter.Register("forgejo", flags, NewExporter)
}

type Exporter struct {
	outputDir string
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	outputDir := config["output_dir"]
	if outputDir == "" {
		outputDir = strings.TrimPrefix(config["location"], name+"://")
	}
	if outputDir == "" {
		return nil, fmt.Errorf("output directory is required")
	}

	return &Exporter{outputDir: filepath.Clean(outputDir)}, nil
}

func (e *Exporter) Origin() string        { return e.outputDir }
func (e *Exporter) Type() string          { return "forgejo" }
func (e *Exporter) Root() string          { return e.outputDir }
func (e *Exporter) Flags() location.Flags { return flags }
func (e *Exporter) Ping(ctx context.Context) error {
	return os.MkdirAll(e.outputDir, 0755)
}
func (e *Exporter) Close(ctx context.Context) error { return nil }

func (e *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	if err := os.MkdirAll(e.outputDir, 0755); err != nil {
		return err
	}

	for record := range records {
		if err := e.writeRecord(record); err != nil {
			results <- record.Error(err)
			continue
		}
		results <- record.Ok()
	}
	return nil
}

func (e *Exporter) writeRecord(record *connectors.Record) error {
	dst, err := safeJoin(e.outputDir, record.Pathname)
	if err != nil {
		return err
	}

	mode := record.FileInfo.Lmode
	if mode.IsDir() {
		return os.MkdirAll(dst, mode.Perm())
	}
	if record.Reader == nil {
		return fmt.Errorf("record %q has no reader", record.Pathname)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	perm := mode.Perm()
	if perm == 0 {
		perm = 0644
	}

	fp, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer fp.Close()

	_, err = io.Copy(fp, record.Reader)
	return err
}

func safeJoin(root, pathname string) (string, error) {
	for _, part := range strings.Split(strings.ReplaceAll(pathname, "\\", "/"), "/") {
		if part == ".." {
			return "", fmt.Errorf("record path escapes output directory: %s", pathname)
		}
	}

	rel := strings.TrimPrefix(path.Clean("/"+pathname), "/")
	if rel == "." || rel == "" {
		return "", fmt.Errorf("invalid empty record path")
	}

	dst := filepath.Join(root, filepath.FromSlash(rel))
	cleanRoot := filepath.Clean(root)
	cleanDst := filepath.Clean(dst)

	inside, err := filepath.Rel(cleanRoot, cleanDst)
	if err != nil {
		return "", err
	}
	if inside == ".." || strings.HasPrefix(inside, ".."+string(os.PathSeparator)) || filepath.IsAbs(inside) {
		return "", fmt.Errorf("record path escapes output directory: %s", pathname)
	}
	return cleanDst, nil
}
