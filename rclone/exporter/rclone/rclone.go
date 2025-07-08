package rclone

import (
	"encoding/json"
	"fmt"
	"context"
	"io"
	"net/http"
	"os"
	stdpath "path"
	"strings"

	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/exporter"

	_ "github.com/rclone/rclone/backend/all"
	"github.com/rclone/rclone/librclone/librclone"
)

type RcloneExporter struct {
	Remote   string
	Base     string
	provider string
}

func NewRcloneExporter(ctx context.Context, opts *exporter.Options, name string, config map[string]string) (exporter.Exporter, error) {
	provider, location, _ := strings.Cut(config["location"], "://")
	remote, base, found := strings.Cut(location, ":")

	if !found {
		return nil, fmt.Errorf("invalid location: %s. Expected format: remote:path/to/dir", location)
	}

	librclone.Initialize()

	return &RcloneExporter{
		Remote:   remote,
		Base:     base,
		provider: provider,
	}, nil
}

// GetPathInBackup returns the full normalized path of a file within the backup.
//
// The resulting path is constructed by joining the base path of the backup (p.base)
// with the provided relative path. If the base path (p.base) is not absolute,
// it is treated as relative to the root ("/").
func (p *RcloneExporter) GetPathInBackup(path string) string {
	path = stdpath.Join(p.Base, path)

	if !stdpath.IsAbs(p.Base) {
		path = "/" + path
	}

	return stdpath.Clean(path)
}

func (p *RcloneExporter) Root() string {
	return p.GetPathInBackup("")
}

func (p *RcloneExporter) CreateDirectory(pathname string) error {
	relativePath := strings.TrimPrefix(pathname, p.GetPathInBackup(""))

	payload := map[string]string{
		"fs":     fmt.Sprintf("%s:%s", p.Remote, p.Base),
		"remote": relativePath,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	body, resp := librclone.RPC("operations/mkdir", string(jsonPayload))
	if resp != http.StatusOK {
		return fmt.Errorf("failed to create directory: %s", body)
	}

	return nil
}

// XXX: it seems there is a race condition when restoring a directory: when
// exporting the first file, the root directory is created. When exporting the
// second file, it is possible that Google Drive doesn't see the root directory
// yet, and creates a new one. This results in a duplicated root directory, with
// some files in the first directory and the rest in the second.
func (p *RcloneExporter) StoreFile(pathname string, fp io.Reader, size int64) error {
	tmpFile, err := os.CreateTemp("", "tempfile-*.tmp")
	if err != nil {
		return err
	}
	defer tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	_, err = io.Copy(tmpFile, fp)
	if err != nil {
		return err
	}

	relativePath := strings.TrimPrefix(pathname, p.GetPathInBackup(""))

	payload := map[string]string{
		"srcFs":     "/",
		"srcRemote": tmpFile.Name(),
		"dstFs":     fmt.Sprintf("%s:%s", p.Remote, p.Base),
		"dstRemote": relativePath,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	body, resp := librclone.RPC("operations/copyfile", string(jsonPayload))

	if resp != http.StatusOK {
		return fmt.Errorf("failed to copy file: %s", body)
	}

	return nil
}

func (p *RcloneExporter) SetPermissions(pathname string, fileinfo *objects.FileInfo) error {
	return nil
}

func (p *RcloneExporter) Close() error {
	return nil
}
