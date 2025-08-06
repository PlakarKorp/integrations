package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/PlakarKorp/integration-rclone/utils"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/storage"
	_ "github.com/rclone/rclone/backend/all" // import all backends
	"github.com/rclone/rclone/librclone/librclone"
)

type RcloneStorage struct {
	Typee    string
	Base     string
	confFile *os.File

	location string
}

func NewRcloneStorage(ctx context.Context, name string, config map[string]string) (storage.Store, error) {
	location, base, found := strings.Cut(config["location"], ":")
	if !found {
		return nil, fmt.Errorf("invalid location: %s. Expected format: location: <provider>://", config["location"])
	}

	utils.CleanPlakarRcloneConf(config)

	typee, found := config["type"]
	if !found {
		return nil, fmt.Errorf("missing type in configuration for %s", name)
	}

	file, err := utils.WriteRcloneConfigFile(typee, config)
	if err != nil {
		return nil, err
	}

	librclone.Initialize()

	return &RcloneStorage{
		Typee:    typee,
		Base:     base,
		confFile: file,

		location: location,
	}, nil
}

func (r *RcloneStorage) mkdir(pathname string) error {
	payload := map[string]string{
		"fs":     fmt.Sprintf("%s:%s", r.Typee, r.Base),
		"remote": pathname,
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

func (r *RcloneStorage) putFile(name string, rd io.Reader) (int64, error) {
	tmpFile, err := os.CreateTemp("", "tempfile-*.tmp")
	if err != nil {
		return 0, err
	}
	defer tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	_, err = io.Copy(tmpFile, rd)
	if err != nil {
		return 0, err
	}

	payload := map[string]string{
		"srcFs":     "/",
		"srcRemote": tmpFile.Name(),
		"dstFs":     fmt.Sprintf("%s:%s", r.Typee, r.Base),
		"dstRemote": name,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	body, resp := librclone.RPC("operations/copyfile", string(jsonPayload))

	if resp != http.StatusOK {
		return 0, fmt.Errorf("failed to put file: %s", body)
	}

	finfo, err := tmpFile.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to stat temporary file: %w", err)
	}

	return finfo.Size(), nil
}

func (r *RcloneStorage) getFile(pathname string) (io.ReadSeekCloser, error) {
	name, err := utils.CreateTempPath("plakar_temp_*")
	if err != nil {
		return nil, err
	}

	payload := map[string]string{
		"srcFs":     fmt.Sprintf("%s:%s", r.Typee, r.Base),
		"srcRemote": pathname,

		"dstFs":     strings.TrimSuffix(name, "/"+path.Base(name)),
		"dstRemote": path.Base(name),
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	body, status := librclone.RPC("operations/copyfile", string(jsonPayload))

	if status != http.StatusOK {
		return nil, fmt.Errorf("failed to get file: %s", body)
	}

	tmpFile, err := os.Open(name)
	if err != nil {
		return nil, err
	}

	return &utils.AutoremoveTmpFile{tmpFile}, nil
}

func (r *RcloneStorage) deleteFile(pathname string) error {
	payload := map[string]string{
		"fs":     fmt.Sprintf("%s:%s", r.Typee, r.Base),
		"remote": pathname,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	body, resp := librclone.RPC("operations/deletefile", string(jsonPayload))
	if resp != http.StatusOK {
		return fmt.Errorf("failed to delete file: %s", body)
	}

	return nil
}

func (r *RcloneStorage) listFolder(pathname string) ([]string, error) {
	payload := map[string]interface{}{
		"fs":     fmt.Sprintf("%s:%s", r.Typee, r.Base),
		"remote": pathname,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	output, status := librclone.RPC("operations/list", string(jsonPayload))
	if status != http.StatusOK {
		return nil, fmt.Errorf("failed to list directory: %s", output)
	}

	var response Response
	err = json.Unmarshal([]byte(output), &response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var files []string
	for _, file := range response.List {
		files = append(files, file.Path)
	}
	return files, nil
}

func (r *RcloneStorage) Create(ctx context.Context, config []byte) error {
	if r.mkdir("") != nil {
		return fmt.Errorf("failed to create root directory")
	}
	entries, err := r.listFolder("")
	if err != nil {
		return fmt.Errorf("failed to list root folder: %w", err)
	}
	for _, entry := range entries {
		if entry == "CONFIG" || entry == "states" || entry == "packfiles" || entry == "locks" {
			return fmt.Errorf("storage %s already exists at %s:%s", entry, r.Typee, r.Base)
		}
	}

	_, err = r.putFile("CONFIG", bytes.NewReader(config))
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}

	err = r.mkdir("states")
	if err != nil {
		return err
	}
	err = r.mkdir("packfiles")
	if err != nil {
		return err
	}
	err = r.mkdir("locks")
	if err != nil {
		return err
	}

	return nil
}

func (r *RcloneStorage) Open(ctx context.Context) ([]byte, error) {
	rd, err := r.getFile("CONFIG")
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer rd.(io.Closer).Close()

	configData, err := io.ReadAll(rd)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	return configData, nil
}

func (r *RcloneStorage) Location(ctx context.Context) (string, error) {
	return r.location, nil
}

func (r *RcloneStorage) Mode(ctx context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (r *RcloneStorage) Size(ctx context.Context) (int64, error) {
	return -1, nil //TODO: Implement size calculation
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

func (r *RcloneStorage) getMacs(name string) ([]objects.MAC, error) {
	entries, err := r.listFolder(name)
	if err != nil {
		return nil, fmt.Errorf("failed to list folder %s: %w", name, err)
	}

	var macs []objects.MAC
	for _, file := range entries {
		mac, err := hex.DecodeString(path.Base(file))
		if err != nil {
			return nil, fmt.Errorf("failed to create MAC from ID %s: %w", file, err)
		}

		macs = append(macs, objects.MAC(mac))
	}

	return macs, nil
}

func (r *RcloneStorage) GetStates(ctx context.Context) ([]objects.MAC, error) {
	return r.getMacs("states")
}

func (r *RcloneStorage) PutState(ctx context.Context, mac objects.MAC, rd io.Reader) (int64, error) {
	return r.putFile(fmt.Sprintf("states/%064x", mac), rd)
}

func (r *RcloneStorage) GetState(ctx context.Context, mac objects.MAC) (io.ReadCloser, error) {
	return r.getFile(fmt.Sprintf("states/%064x", mac))
}

func (r *RcloneStorage) DeleteState(ctx context.Context, mac objects.MAC) error {
	return r.deleteFile(fmt.Sprintf("states/%064x", mac))
}

func (r *RcloneStorage) GetPackfiles(ctx context.Context) ([]objects.MAC, error) {
	return r.getMacs("packfiles")
}

func (r *RcloneStorage) PutPackfile(ctx context.Context, mac objects.MAC, rd io.Reader) (int64, error) {
	return r.putFile(fmt.Sprintf("packfiles/%064x", mac), rd)
}

func (r *RcloneStorage) GetPackfile(ctx context.Context, mac objects.MAC) (io.ReadCloser, error) {
	return r.getFile(fmt.Sprintf("packfiles/%064x", mac))
}

func limitReadCloser(r io.ReadCloser, n int64) io.ReadCloser {
	return &limitedReadCloser{io.LimitReader(r, n), r}
}

type limitedReadCloser struct {
	io.Reader
	io.Closer
}

func (r *RcloneStorage) GetPackfileBlob(ctx context.Context, mac objects.MAC, offset uint64, length uint32) (io.ReadCloser, error) {
	rd, err := r.getFile(fmt.Sprintf("packfiles/%064x", mac))
	if err != nil {
		return nil, err
	}

	_, err = rd.Seek(int64(offset), io.SeekStart)
	if err != nil {
		return nil, err
	}

	return limitReadCloser(rd, int64(length)), nil
}

func (r *RcloneStorage) DeletePackfile(ctx context.Context, mac objects.MAC) error {
	return r.deleteFile(fmt.Sprintf("packfiles/%064x", mac))
}

func (r *RcloneStorage) GetLocks(ctx context.Context) ([]objects.MAC, error) {
	return r.getMacs("locks")
}

func (r *RcloneStorage) PutLock(ctx context.Context, lockID objects.MAC, rd io.Reader) (int64, error) {
	return r.putFile(fmt.Sprintf("locks/%064x", lockID), rd)
}

func (r *RcloneStorage) GetLock(ctx context.Context, lockID objects.MAC) (io.ReadCloser, error) {
	return r.getFile(fmt.Sprintf("locks/%064x", lockID))
}

func (r *RcloneStorage) DeleteLock(ctx context.Context, lockID objects.MAC) error {
	return r.deleteFile(fmt.Sprintf("locks/%064x", lockID))
}

func (r *RcloneStorage) Close(ctx context.Context) error {
	utils.DeleteTempConf(r.confFile.Name())
	librclone.Finalize()
	return nil
}
