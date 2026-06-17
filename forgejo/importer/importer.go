package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

const flags = location.FLAG_LOCALFS | location.FLAG_STREAM | location.FLAG_NEEDACK

func init() {
	importer.Register("forgejo", flags, NewImporter)
}

type Importer struct {
	opts       *connectors.Options
	binary     string
	workPath   string
	configPath string
	customPath string
	tempDir    string
	dumpType   string
}

type manifest struct {
	Connector      string    `json:"connector"`
	Archive        string    `json:"archive"`
	DumpType       string    `json:"dump_type"`
	ForgejoVersion string    `json:"forgejo_version,omitempty"`
	WorkPath       string    `json:"work_path,omitempty"`
	ConfigPath     string    `json:"config_path,omitempty"`
	CustomPath     string    `json:"custom_path,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	workPath := strings.TrimPrefix(config["location"], name+"://")
	if config["work_path"] != "" {
		workPath = config["work_path"]
	}

	dumpType := config["dump_type"]
	if dumpType == "" {
		dumpType = config["type"]
	}
	if dumpType == "" {
		dumpType = "zip"
	}
	dumpType, err := normalizeDumpType(dumpType)
	if err != nil {
		return nil, err
	}

	binary := config["binary"]
	if binary == "" {
		binary = "forgejo"
	}

	return &Importer{
		opts:       opts,
		binary:     binary,
		workPath:   workPath,
		configPath: config["config"],
		customPath: config["custom_path"],
		tempDir:    config["tempdir"],
		dumpType:   dumpType,
	}, nil
}

func (i *Importer) Origin() string {
	if i.workPath != "" {
		return i.workPath
	}
	if i.opts != nil {
		return i.opts.Hostname
	}
	return "localhost"
}

func (i *Importer) Type() string          { return "forgejo" }
func (i *Importer) Root() string          { return "/" }
func (i *Importer) Flags() location.Flags { return flags }
func (i *Importer) Ping(ctx context.Context) error {
	if _, err := exec.LookPath(i.binary); err != nil {
		return err
	}
	return nil
}
func (i *Importer) Close(ctx context.Context) error { return nil }

func (i *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	tempRoot, err := os.MkdirTemp(i.tempDir, "plakar-forgejo-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempRoot)

	archiveName := "forgejo-dump." + archiveExtension(i.dumpType)
	archivePath := filepath.Join(tempRoot, archiveName)
	if err := i.runDump(ctx, archivePath); err != nil {
		return err
	}

	version := i.forgejoVersion(ctx)
	meta, err := json.MarshalIndent(manifest{
		Connector:      "forgejo",
		Archive:        "/" + archiveName,
		DumpType:       i.dumpType,
		ForgejoVersion: version,
		WorkPath:       i.workPath,
		ConfigPath:     i.configPath,
		CustomPath:     i.customPath,
		CreatedAt:      time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := sendMemoryRecord(ctx, records, results, "/manifest.json", meta); err != nil {
		return err
	}

	if err := i.sendFileRecord(ctx, records, results, archivePath, "/"+archiveName); err != nil {
		return err
	}
	return nil
}

func (i *Importer) runDump(ctx context.Context, archivePath string) error {
	args := i.globalArgs()
	args = append(args, "dump", "--file", archivePath, "--type", i.dumpType, "--quiet")
	if i.tempDir != "" {
		args = append(args, "--tempdir", i.tempDir)
	}

	cmd := exec.CommandContext(ctx, i.binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("forgejo dump: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (i *Importer) globalArgs() []string {
	var args []string
	if i.workPath != "" {
		args = append(args, "--work-path", i.workPath)
	}
	if i.configPath != "" {
		args = append(args, "--config", i.configPath)
	}
	if i.customPath != "" {
		args = append(args, "--custom-path", i.customPath)
	}
	return args
}

func (i *Importer) forgejoVersion(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, i.binary, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (i *Importer) sendFileRecord(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result, filename, snapshotPath string) error {
	info, err := os.Stat(filename)
	if err != nil {
		return err
	}

	fi := objects.FileInfo{
		Lname:    path.Base(snapshotPath),
		Lsize:    info.Size(),
		Lmode:    info.Mode(),
		LmodTime: info.ModTime(),
		Lnlink:   1,
	}

	return sendRecord(ctx, records, results, connectors.NewRecord(snapshotPath, "", fi, nil, func() (io.ReadCloser, error) {
		return os.Open(filename)
	}))
}

func sendMemoryRecord(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result, snapshotPath string, data []byte) error {
	fi := objects.FileInfo{
		Lname:    path.Base(snapshotPath),
		Lsize:    int64(len(data)),
		Lmode:    fs.FileMode(0644),
		LmodTime: time.Now().UTC(),
		Lnlink:   1,
	}
	return sendRecord(ctx, records, results, connectors.NewRecord(snapshotPath, "", fi, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}))
}

func sendRecord(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result, record *connectors.Record) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- record:
	}

	if results == nil {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case result, ok := <-results:
		if ok && result != nil && result.Err != nil {
			return result.Err
		}
		return nil
	}
}

func archiveExtension(dumpType string) string {
	switch dumpType {
	case "zip":
		return "zip"
	case "tar":
		return "tar"
	case "tar.sz":
		return "tar.sz"
	case "tar.gz":
		return "tar.gz"
	case "tar.bz2":
		return "tar.bz2"
	case "tar.xz":
		return "tar.xz"
	case "tar.zst":
		return "tar.zst"
	case "tar.br":
		return "tar.br"
	case "tar.lz4":
		return "tar.lz4"
	default:
		panic("unsupported dump type: " + dumpType)
	}
}

func normalizeDumpType(dumpType string) (string, error) {
	dumpType = strings.ToLower(strings.TrimSpace(dumpType))
	switch dumpType {
	case "zip", "tar", "tar.sz", "tar.gz", "tar.xz", "tar.bz2", "tar.br", "tar.lz4", "tar.zst":
		return dumpType, nil
	default:
		return "", fmt.Errorf("unsupported forgejo dump type %q", dumpType)
	}
}
