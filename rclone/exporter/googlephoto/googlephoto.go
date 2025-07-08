package googlephoto

import (
	"encoding/json"
	"fmt"
	"context"
	"github.com/PlakarKorp/kloset/snapshot/exporter"
	"io"
	"net/http"
	"os"
	stdpath "path"
	"strings"

	"github.com/rclone/rclone/librclone/librclone"

	"github.com/PlakarKorp/integration-rclone/exporter/rclone"
)

type GooglePhotoExporter struct {
	*rclone.RcloneExporter
}

func NewGooglePhotoExporter(ctx context.Context, opts *exporter.Options, name string, config map[string]string) (exporter.Exporter, error) {
	exp, err := rclone.NewRcloneExporter(ctx, opts, name, config)
	if err != nil {
		return nil, err
	}
	return &GooglePhotoExporter{RcloneExporter: exp.(*rclone.RcloneExporter)}, nil
}

// The operation mkdir is a no-op for Google Photos
func (p *GooglePhotoExporter) CreateDirectory(pathname string) error {
	return nil
}

func (p *GooglePhotoExporter) StoreFile(pathname string, fp io.Reader, size int64) error {
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
	if p.Base != "" {
		relativePath = p.Base + "/" + relativePath
	}

	payload := map[string]string{
		"srcFs":     "/",
		"srcRemote": tmpFile.Name(),
		"dstFs":     p.Remote + ":",
		"dstRemote": func() string {
			if strings.HasPrefix(relativePath, "media/") {
				return "upload/" + stdpath.Base(relativePath)
			}
			if strings.HasPrefix(relativePath, "feature/") {
				return "album/FAVORITE/" + stdpath.Base(relativePath)
			}
			return relativePath
		}(),
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
