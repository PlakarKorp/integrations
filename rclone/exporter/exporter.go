package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	stdpath "path"
	"strings"

	"github.com/PlakarKorp/integrations/rclone/utils"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"

	_ "github.com/rclone/rclone/backend/all"
	"github.com/rclone/rclone/librclone/librclone"

	"golang.org/x/sync/errgroup"
)

type RcloneExporter struct {
	Typee    string
	Base     string
	confFile *os.File

	maxConcurrency int
}

func NewRcloneExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	_, base, found := strings.Cut(config["location"], ":")
	if !found {
		return nil, fmt.Errorf("invalid location: %s. Expected format: location: <provider>://", config["location"])
	}

	utils.CleanPlakarRcloneConf(config)

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
		Typee:          typee,
		Base:           base,
		confFile:       file,
		maxConcurrency: opts.MaxConcurrency,
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

func (p *RcloneExporter) Root() string          { return p.GetPathInBackup("") }
func (p *RcloneExporter) Origin() string        { return p.Typee } // WRONG
func (p *RcloneExporter) Type() string          { return p.Typee }
func (p *RcloneExporter) Flags() location.Flags { return 0 }

func (p *RcloneExporter) Ping(ctx context.Context) error {
	return nil
}

func (p *RcloneExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
	defer close(results)

	var g errgroup.Group
	g.SetLimit(p.maxConcurrency)

	for record := range records {
		if record.Err != nil {
			results <- record.Ok()
			continue
		}

		if record.IsXattr || record.FileInfo.Lmode&os.ModeSymlink != 0 {
			results <- record.Ok()
			continue
		}

		pathname := stdpath.Join(p.Root(), record.Pathname)
		if record.FileInfo.Lmode.IsDir() {
			if err := p.mkdir(ctx, pathname); err != nil {
				results <- record.Error(err)
			} else {
				results <- record.Ok()
			}

			continue
		}

		g.Go(func() error {
			if err := p.storeFile(ctx, pathname, record); err != nil {
				results <- record.Error(err)
			} else {
				results <- record.Ok()
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil && ret == nil {
		ret = err
	}

	return ret
}

func (p *RcloneExporter) mkdir(ctx context.Context, pathname string) error {
	if p.Typee == "googlephotos" {
		return nil
	}

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
func (p *RcloneExporter) storeFile(ctx context.Context, pathname string, record *connectors.Record) error {
	tmpFile, err := os.CreateTemp("", "tempfile-*.tmp")
	if err != nil {
		return err
	}
	defer tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	_, err = io.Copy(tmpFile, record.Reader)
	if err != nil {
		return err
	}

	relativePath := strings.TrimPrefix(pathname, p.GetPathInBackup(""))

	var dstFs string = fmt.Sprintf("%s:%s", p.Typee, p.Base)
	var dstRemoteFunc func() string = func() string {
		return relativePath
	}
	if p.Typee == "googlephotos" {
		dstFs = p.Typee + ":"
		dstRemoteFunc = func() string {
			if strings.HasPrefix(relativePath, "media/") {
				return "upload/" + stdpath.Base(relativePath)
			}
			if strings.HasPrefix(relativePath, "feature/") {
				return "album/FAVORITE/" + stdpath.Base(relativePath)
			}
			return relativePath
		}
	}

	payload := map[string]string{
		"srcFs":     "/",
		"srcRemote": tmpFile.Name(),
		"dstFs":     dstFs,
		"dstRemote": dstRemoteFunc(),
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

func (p *RcloneExporter) Close(ctx context.Context) error {
	if p.confFile != nil {
		utils.DeleteTempConf(p.confFile.Name())
	}
	librclone.Finalize()
	return nil
}
