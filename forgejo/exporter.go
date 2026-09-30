package forgejo

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	iexporter "github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	iexporter.Register("forgejo", 0, NewExporter)
}

type Exporter struct {
	cfg config
}

func NewExporter(_ context.Context, _ *connectors.Options, _ string, values map[string]string) (iexporter.Exporter, error) {
	cfg, err := parseExporterConfig(values)
	if err != nil {
		return nil, err
	}
	return &Exporter{cfg: cfg}, nil
}

func (e *Exporter) Origin() string        { return e.cfg.targetDir }
func (e *Exporter) Type() string          { return "forgejo" }
func (e *Exporter) Root() string          { return e.cfg.targetDir }
func (e *Exporter) Flags() location.Flags { return location.FLAG_LOCALFS }
func (e *Exporter) Ping(_ context.Context) error {
	return os.MkdirAll(e.cfg.targetDir, 0755)
}
func (e *Exporter) Close(_ context.Context) error { return nil }

func (e *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case record, ok := <-records:
			if !ok {
				return nil
			}
			if record.Err != nil {
				results <- record.Error(record.Err)
				continue
			}
			if record.FileInfo.Lmode.IsDir() {
				results <- record.Ok()
				continue
			}
			if err := e.restore(record); err != nil {
				results <- record.Error(err)
				continue
			}
			results <- record.Ok()
		}
	}
}

func (e *Exporter) restore(record *connectors.Record) error {
	if record.Reader == nil {
		return fmt.Errorf("record %s has no reader", record.Pathname)
	}

	name := filepath.Base(record.Pathname)
	switch {
	case strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz"):
		return extractTarGz(record.Reader, e.cfg.targetDir)
	case strings.HasSuffix(name, ".tar"):
		return extractTar(record.Reader, e.cfg.targetDir)
	case strings.HasSuffix(name, ".zip"):
		return extractZip(record.Reader, e.cfg.targetDir)
	default:
		return fmt.Errorf("unexpected Forgejo backup record %q", record.Pathname)
	}
}

func extractTarGz(reader io.Reader, targetDir string) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer gz.Close()
	return extractTar(gz, targetDir)
}

func extractTar(reader io.Reader, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}
		target, err := safeJoin(targetDir, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeFile(target, os.FileMode(header.Mode), tr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry %q type %d", header.Name, header.Typeflag)
		}
	}
}

func extractZip(reader io.Reader, targetDir string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("reading zip archive: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("opening zip archive: %w", err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	for _, file := range zr.File {
		target, err := safeJoin(targetDir, file.Name)
		if err != nil {
			return err
		}
		info := file.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, info.Mode()); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported zip entry %q mode %s", file.Name, info.Mode())
		}
		src, err := file.Open()
		if err != nil {
			return fmt.Errorf("opening zip entry %q: %w", file.Name, err)
		}
		err = writeFile(target, info.Mode(), src)
		closeErr := src.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func writeFile(target string, mode os.FileMode, reader io.Reader) error {
	if mode == 0 {
		mode = 0644
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func safeJoin(baseDir, name string) (string, error) {
	cleanName := filepath.Clean(name)
	if cleanName == "." || filepath.IsAbs(cleanName) || strings.HasPrefix(cleanName, ".."+string(os.PathSeparator)) || cleanName == ".." {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}

	base, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, cleanName)
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return target, nil
}
