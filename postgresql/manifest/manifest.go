package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

type ManifestOptions struct {
	SchemaOnly bool `json:"schema_only"`
	DataOnly   bool `json:"data_only"`
	Compress   bool `json:"compress"`
}

type Manifest struct {
	Version          int              `json:"version"`
	CreatedAt        time.Time        `json:"created_at"`
	Connector        string           `json:"connector"`
	Host             string           `json:"host"`
	Port             string           `json:"port"`
	ServerVersion    string           `json:"server_version"`
	ServerVersionNum int              `json:"server_version_num"`
	Database         string           `json:"database,omitempty"`
	DumpFormat       string           `json:"dump_format"`
	Options          *ManifestOptions `json:"options,omitempty"`
}

// ServerVersion queries the PostgreSQL server for its version string and
// numeric version. psqlBin is the path to the psql binary; connectDB is
// the database used for the connection.
func ServerVersion(ctx context.Context, psqlBin, host, port, connectDB, username string, env []string) (string, int, error) {
	args := []string{
		"-h", host, "-p", port, "-d", connectDB, "-w",
		"-t", "-A", "-F", "|", "--no-psqlrc",
		"-c", "SELECT current_setting('server_version'), current_setting('server_version_num')",
	}
	if username != "" {
		args = append(args, "-U", username)
	}
	cmd := exec.CommandContext(ctx, psqlBin, args...)
	cmd.Stdin = nil
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", 0, fmt.Errorf("querying server version: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("unexpected server version output: %q", string(out))
	}
	versionNum, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("parsing server_version_num %q: %w", parts[1], err)
	}
	return parts[0], versionNum, nil
}

// EmitManifest serialises m as /manifest.json and sends it on records.
func EmitManifest(ctx context.Context, records chan<- *connectors.Record, m *Manifest) error {
	m.Version = 1
	now := time.Now().UTC()
	m.CreatedAt = now

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	fileinfo := objects.FileInfo{
		Lname:    "manifest.json",
		Lsize:    int64(len(data)),
		Lmode:    0444,
		LmodTime: now,
	}
	readerFunc := func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- connectors.NewRecord("/manifest.json", "", fileinfo, nil, readerFunc):
	}
	return nil
}
