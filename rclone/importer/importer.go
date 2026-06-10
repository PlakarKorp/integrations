package importer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	stdpath "path"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/integrations/rclone/utils"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"

	_ "github.com/rclone/rclone/backend/all" // import all backends
	"github.com/rclone/rclone/librclone/librclone"
)

func ggdPhotoSpeCase(filename string) error {
	if strings.HasPrefix(filename, "media/") {
		if strings.HasPrefix(filename, "media/all") {
			return nil
		}
		return fmt.Errorf("skipping %s", filename)
	}
	if filename == "upload" {
		return fmt.Errorf("skipping %s", filename)
	}
	return nil
}

type Response struct {
	List []struct {
		Path     string `json:"Path"`
		Name     string `json:"Name"`
		Size     int64  `json:"Size"`
		MimeType string `json:"MimeType"`
		ModTime  string `json:"ModTime"`
		IsDir    bool   `json:"isDir"`
		ID       string `json:"ID"`
	} `json:"list"`
}

type RcloneImporter struct {
	Typee    string
	Base     string
	confFile *os.File
}

// NewRcloneImporter creates a new RcloneImporter instance. It expects the location
// to be in the format "remote:path/to/dir". The path is optional, but the remote
// storage location is required, so the colon separator is always expected.
func NewRcloneImporter(ctx context.Context, opts *connectors.Options, providerName string, config map[string]string) (importer.Importer, error) {
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

	return &RcloneImporter{
		Typee:    typee,
		Base:     base,
		confFile: file,
	}, nil
}

func (p *RcloneImporter) Root() string          { return p.GetPathInBackup("") }
func (p *RcloneImporter) Origin() string        { return p.Typee } // WRONG
func (p *RcloneImporter) Type() string          { return p.Typee }
func (p *RcloneImporter) Flags() location.Flags { return 0 }

func (p *RcloneImporter) Ping(ctx context.Context) error {
	return nil
}

func (p *RcloneImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	// Inline scanRecursive here so that we get a chance to catch an early
	// error and abort the import.
	response, err := p.ListFolder(records, "")
	if err != nil {
		return err
	}

	wg := &sync.WaitGroup{}
	p.scanFolder(records, "", response, wg)

	wg.Wait()
	return nil
}

// GetPathInBackup returns the full normalized path of a file within the backup.
//
// The resulting path is constructed by joining the base path of the backup (p.base)
// with the provided relative path. If the base path (p.base) is not absolute,
// it is treated as relative to the root ("/").
func (p *RcloneImporter) GetPathInBackup(path string) string {
	path = stdpath.Join(p.Base, path)

	if !stdpath.IsAbs(p.Base) {
		path = "/" + path
	}

	return stdpath.Clean(path)
}

func (p *RcloneImporter) scanRecursive(results chan<- *connectors.Record, path string, wg *sync.WaitGroup) {
	response, _ := p.ListFolder(results, path)
	p.scanFolder(results, path, response, wg)
}

func (p *RcloneImporter) ListFolder(results chan<- *connectors.Record, path string) (Response, error) {
	payload := map[string]string{
		"fs":     fmt.Sprintf("%s:%s", p.Typee, p.Base),
		"remote": path,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		results <- connectors.NewError(p.GetPathInBackup(path), err)
		return Response{}, err
	}

	output, status := librclone.RPC("operations/list", string(jsonPayload))
	if status != http.StatusOK {
		results <- connectors.NewError(p.GetPathInBackup(path), fmt.Errorf("failed to list directory: %s", output))
		return Response{}, err
	}

	var response Response
	err = json.Unmarshal([]byte(output), &response)
	if err != nil {
		results <- connectors.NewError(p.GetPathInBackup(path), err)
		return Response{}, err
	}

	return response, nil
}

func (p *RcloneImporter) scanFolder(results chan<- *connectors.Record, path string, response Response, wg *sync.WaitGroup) {
	for _, file := range response.List {
		wg.Add(1)
		go func() {
			defer wg.Done()

			if p.Typee == "googlephotos" {
				if ggdPhotoSpeCase(file.Path) != nil {
					return
				}
			}

			// Should never happen, but just in case let's fallback to the Unix epoch
			parsedTime, err := time.Parse(time.RFC3339, file.ModTime)
			if err != nil {
				parsedTime = time.Unix(0, 0).UTC()
			}

			if file.IsDir {
				wg.Add(1)
				go func() {
					defer wg.Done()
					p.scanRecursive(results, file.Path, wg)
				}()

				results <- connectors.NewRecord(
					p.GetPathInBackup(file.Path),
					"",
					objects.FileInfo{
						Lname:    stdpath.Base(file.Name),
						Lmode:    0700 | os.ModeDir,
						LmodTime: parsedTime,
					},
					nil,
					func() (io.ReadCloser, error) {
						return nil, nil
					},
				)
			} else {
				results <- connectors.NewRecord(
					p.GetPathInBackup(file.Path),
					"",
					objects.FileInfo{
						Lname:    stdpath.Base(file.Path),
						Lsize:    file.Size,
						Lmode:    0600,
						LmodTime: parsedTime,
						Ldev:     1,
					},
					nil,
					func() (io.ReadCloser, error) {
						return p.NewReader(file.Path)
					},
				)
			}

		}()
	}
}

func nextRandom() string {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}

func createTempPath(originalPath string) (path string, err error) {
	tmpPath := os.TempDir() + "/" + originalPath
	prefix, suffix := "", ""
	if i := strings.LastIndex(tmpPath, "*"); i >= 0 {
		prefix, suffix = tmpPath[:i], tmpPath[i+1:]
	} else {
		prefix = tmpPath
	}

	for i := 0; i < 10000; i++ {
		name := prefix + nextRandom() + suffix
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return name, nil
		}
	}
	return "", fmt.Errorf("failed to find a folder to create the temporary file")
}

// AutoremoveTmpFile is a wrapper around an os.File that removes the file when it's closed.
type AutoremoveTmpFile struct {
	*os.File
}

func (file *AutoremoveTmpFile) Close() error {
	//defer os.Remove(file.Name())
	return file.File.Close()
}

func (p *RcloneImporter) NewReader(pathname string) (io.ReadCloser, error) {
	// pathname is an absolute path within the backup. Let's convert it to a
	// relative path to the base path.
	relativePath := strings.TrimPrefix(pathname, p.GetPathInBackup(""))
	name, err := createTempPath("plakar_temp_*")
	if err != nil {
		return nil, err
	}

	payload := map[string]string{
		"srcFs":     fmt.Sprintf("%s:%s", p.Typee, p.Base),
		"srcRemote": strings.TrimPrefix(relativePath, "/"),

		"dstFs":     strings.TrimSuffix(name, "/"+stdpath.Base(name)),
		"dstRemote": stdpath.Base(name),
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	body, status := librclone.RPC("operations/copyfile", string(jsonPayload))

	if status != http.StatusOK {
		return nil, fmt.Errorf("failed to copy file: %s", body)
	}

	tmpFile, err := os.Open(name)
	if err != nil {
		return nil, err
	}

	return &AutoremoveTmpFile{tmpFile}, nil
}

func (p *RcloneImporter) Close(ctx context.Context) error {
	utils.DeleteTempConf(p.confFile.Name())
	librclone.Finalize()
	return nil
}
