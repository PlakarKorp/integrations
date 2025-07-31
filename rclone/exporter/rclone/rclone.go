package rclone

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	stdpath "path"
	"strings"

	"github.com/PlakarKorp/integration-rclone/utils"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/exporter"

	_ "github.com/rclone/rclone/backend/all"
	"github.com/rclone/rclone/librclone/librclone"
)

type RcloneExporter struct {
	Typee    string
	Base     string
	confFile *os.File
}

func NewRcloneExporter(ctx context.Context, opts *exporter.Options, name string, config map[string]string) (exporter.Exporter, error) {
	_, base, found := strings.Cut(config["location"], ":")
	if !found {
		return nil, fmt.Errorf("invalid location: %s. Expected format: location: <provider>://", config["location"])
	}

	typee, found := config["type"]
	if !found {
		return nil, fmt.Errorf("missing type in configuration")
	}

	file, err := utils.WriteRcloneConfigFile(typee, config)
	if err != nil {
		return nil, err
	}

	librclone.Initialize()

	return &RcloneExporter{
		Typee:    typee,
		Base:     base,
		confFile: file,
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

func (p *RcloneExporter) Root(ctx context.Context) (string, error) {
	return p.GetPathInBackup(""), nil
}

func (p *RcloneExporter) CreateDirectory(ctx context.Context, pathname string) error {
	relativePath := strings.TrimPrefix(pathname, p.GetPathInBackup(""))

	payload := map[string]string{
		"fs":     fmt.Sprintf("%s:%s", p.Typee, p.Base),
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
func (p *RcloneExporter) StoreFile(ctx context.Context, pathname string, fp io.Reader, size int64) error {
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
		"dstFs":     fmt.Sprintf("%s:%s", p.Typee, p.Base),
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

func (p *RcloneExporter) SetPermissions(ctx context.Context, pathname string, fileinfo *objects.FileInfo) error {
	return nil
}

func (p *RcloneExporter) Close(ctx context.Context) error {
	if p.confFile != nil {
		utils.DeleteTempConf(p.confFile.Name())
	}
	librclone.Finalize()
	return nil
}
