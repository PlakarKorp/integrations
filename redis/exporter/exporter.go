package exporter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/location"
)

type Exporter struct {
	proto, output string
	force         bool
}

func New(proto string, config map[string]string) (*Exporter, error) {
	out := strings.TrimSpace(config["output"])
	if out == "" {
		loc := strings.TrimPrefix(config["location"], proto+"://")
		if loc != "" && loc != config["location"] {
			out = loc
		}
	}
	if out == "" {
		return nil, fmt.Errorf("output is required (path to restored dump.rdb)")
	}
	force := false
	if v := config["force"]; v != "" {
		switch strings.ToLower(v) {
		case "1", "t", "true", "yes":
			force = true
		case "0", "f", "false", "no":
			force = false
		default:
			return nil, fmt.Errorf("invalid value for force: %q", v)
		}
	}
	return &Exporter{proto: proto, output: out, force: force}, nil
}

func (e *Exporter) Origin() string              { return e.output }
func (e *Exporter) Type() string                { return e.proto }
func (e *Exporter) Root() string                { return "/" }
func (e *Exporter) Flags() location.Flags       { return 0 }
func (e *Exporter) Ping(context.Context) error  { return nil }
func (e *Exporter) Close(context.Context) error { return nil }

func (e *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)
	for record := range records {
		if record.Err != nil {
			results <- record.Ok()
			continue
		}
		if record.FileInfo.Lmode.IsDir() {
			results <- record.Ok()
			continue
		}
		if filepath.Base(record.Pathname) != "dump.rdb" {
			results <- record.Ok()
			continue
		}
		if err := e.restore(ctx, record); err != nil {
			results <- record.Error(err)
		} else {
			results <- record.Ok()
		}
	}
	return nil
}

func (e *Exporter) restore(ctx context.Context, record *connectors.Record) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if !e.force {
		if _, err := os.Stat(e.output); err == nil {
			return fmt.Errorf("refusing to overwrite %s without force=true", e.output)
		}
		if err := os.MkdirAll(filepath.Dir(e.output), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(e.output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, record.Reader)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.output), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(e.output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, record.Reader)
	return err
}
