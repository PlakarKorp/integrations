package rclone

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"context"
	stdpath "path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/importer"

	_ "github.com/rclone/rclone/backend/all" // import all backends
	"github.com/rclone/rclone/librclone/librclone"
	"github.com/rclone/rclone/fs/config"
)

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
	Typee     string
	Base     string
	confFile *os.File

	Ino uint64
}

// NewRcloneImporter creates a new RcloneImporter instance. It expects the location
// to be in the format "remote:path/to/dir". The path is optional, but the remote
// storage location is required, so the colon separator is always expected.
func NewRcloneImporter(ctx context.Context, opts *importer.Options, providerName string, config map[string]string) (importer.Importer, error) {
	_, base, found := strings.Cut(config["location"], ":")
	if !found {
		return nil, fmt.Errorf("invalid location: %s. Expected format: location: <provider>://", config["location"])
	}

	typee, found := config["type"]
	if !found {
		return nil, fmt.Errorf("missing type in configuration")
	}

	file, err := writeRcloneConfigFile(typee, config)
	if err != nil {
		return nil, err
	}

	librclone.Initialize()

	return &RcloneImporter{
		Typee:	 typee,
		Base:     base,
		confFile: file,
	}, nil
}

func (p *RcloneImporter) Scan() (<-chan *importer.ScanResult, error) {
	results := make(chan *importer.ScanResult, 1000)
	var wg sync.WaitGroup

	go func() {
		p.GenerateBaseDirectories(results)
		p.scanRecursive(results, "", &wg)
		wg.Wait()
		close(results)
	}()

	return results, nil
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

// GenerateBaseDirectories sends all parent directories of the base path in
// reverse order to the provided results channel.
//
// For example, if the base is "remote:/path/to/dir", this function generates
// the directories "/path/to/dir", "/path/to", "/path", and "/".
func (p *RcloneImporter) GenerateBaseDirectories(results chan *importer.ScanResult) {
	parts := generatePathComponents(p.GetPathInBackup(""))

	for _, part := range parts {
		results <- importer.NewScanRecord(
			part,
			"",
			objects.NewFileInfo(
				stdpath.Base(part),
				0,
				0700|os.ModeDir,
				time.Unix(0, 0).UTC(),
				0,
				atomic.AddUint64(&p.Ino, 1),
				0,
				0,
				0,
			),
			nil,
			func() (io.ReadCloser, error) {
				return nil, nil
			},
		)
	}
}

// generatePathComponents is a helper function that returns a slice of strings
// containing all the hierarchical components of an absolute path, starting
// from the full path down to the root.
//
// The path given as an argument must be an absolute clean path within the
// backup.
//
// Example:
//
//	Input:  "/path/to/dir"
//	Output: []string{"/path/to/dir", "/path/to", "/path", "/"}
//
//	Input:  "/relative/path"
//	Output: []string{"/relative/path", "/relative", "/"}
//
//	Input:  "/"
//	Output: []string{"/"}
func generatePathComponents(path string) []string {
	components := []string{}
	tmp := path

	for {
		components = append(components, tmp)
		parent := stdpath.Dir(tmp)
		if parent == tmp { // Reached the root
			break
		}
		tmp = parent
	}
	return components
}

func (p *RcloneImporter) scanRecursive(results chan *importer.ScanResult, path string, wg *sync.WaitGroup) {
	results, response, err := p.ListFolder(results, path)
	if err {
		return
	}
	p.scanFolder(results, path, response, wg)
}

func (p *RcloneImporter) ListFolder(results chan *importer.ScanResult, path string) (chan *importer.ScanResult, Response, bool) {
	payload := map[string]interface{}{
		"fs":     fmt.Sprintf("%s:%s", p.Typee, p.Base),
		"remote": path,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		results <- importer.NewScanError(p.GetPathInBackup(path), err)
		return nil, Response{}, true
	}

	output, status := librclone.RPC("operations/list", string(jsonPayload))
	if status != http.StatusOK {
		results <- importer.NewScanError(p.GetPathInBackup(path), fmt.Errorf("failed to list directory: %s", output))
		return nil, Response{}, true
	}

	var response Response
	err = json.Unmarshal([]byte(output), &response)
	if err != nil {
		results <- importer.NewScanError(p.GetPathInBackup(path), err)
		return nil, Response{}, true
	}
	return results, response, false
}

func (p *RcloneImporter) scanFolder(results chan *importer.ScanResult, path string, response Response, wg *sync.WaitGroup) {
	for _, file := range response.List {
		wg.Add(1)
		go func() {
			defer wg.Done()

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

				results <- importer.NewScanRecord(
					p.GetPathInBackup(file.Path),
					"",
					objects.NewFileInfo(
						stdpath.Base(file.Name),
						0,
						0700|os.ModeDir,
						parsedTime,
						0,
						atomic.AddUint64(&p.Ino, 1),
						0,
						0,
						0,
					),
					nil,
					func() (io.ReadCloser, error) {
						return nil, nil
					},
				)
			} else {
				filesize := file.Size

				fi := objects.NewFileInfo(
					stdpath.Base(file.Path),
					filesize,
					0600,
					parsedTime,
					1,
					atomic.AddUint64(&p.Ino, 1),
					0,
					0,
					0,
				)

				results <- importer.NewScanRecord(
					p.GetPathInBackup(file.Path),
					"",
					fi,
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
	defer os.Remove(file.Name())
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

func (p *RcloneImporter) Close() error {
	deleteTempConf(p.confFile.Name())
	librclone.Finalize()
	return nil
}

func (p *RcloneImporter) Root() string {
	return p.GetPathInBackup("")
}

func (p *RcloneImporter) Origin() string {
	return p.Typee
}

func (p *RcloneImporter) Type() string {
	return p.Typee
}

func writeRcloneConfigFile(name string, remoteMap map[string]string) (*os.File, error) {
	file, err := createTempConf()
	_, err = fmt.Fprintf(file, "[%s]\n", name)
	if err != nil {
		return nil, err
	}
	for k, v := range remoteMap {
		_, err = fmt.Fprintf(file, "%s = %s\n", k, v)
	}
	return file, nil
}

func createTempConf() (*os.File, error) {
	tempFile, err := os.CreateTemp("", "rclone-*.conf")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary config file: %w", err)
	}
	err = config.SetConfigPath(tempFile.Name())
	if err != nil {
		return nil, err
	}
	return tempFile, nil
}

func deleteTempConf(name string) {
	err := os.Remove(name)
	if err != nil {
		fmt.Printf("Error removing temporary file: %v\n", err)
	}
}
