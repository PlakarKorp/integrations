package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/PlakarKorp/integration-rclone/utils"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/storage"
	_ "github.com/rclone/rclone/backend/all" // import all backends
	"github.com/rclone/rclone/librclone/librclone"
	"io"
	"net/http"
	"os"
	stdpath "path"
	"strings"
)

type RcloneStorage struct {
	Typee    string
	Base     string
	confFile *os.File

	location string
}

func NewRcloneStorage(ctx context.Context, name string, config map[string]string) (storage.Store, error) {
	_, base, found := strings.Cut(config["location"], "://")
	if !found {
		return nil, fmt.Errorf("invalid location: %s. Expected format: remote:path/to/dir", name+"://"+config["location"])
	}

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

		location: config["location"],
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

		"dstFs":     strings.TrimSuffix(name, "/"+stdpath.Base(name)),
		"dstRemote": stdpath.Base(name),
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

func (r *RcloneStorage) Create(ctx context.Context, config []byte) error {

	rd, err := r.getFile("CONFIG")
	if err == nil {
		rd.Close()
		return fmt.Errorf("storage already exists at %s:%s", r.Typee, r.Base)
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

func (r *RcloneStorage) Location() string {
	return r.location
}

func (r *RcloneStorage) Mode() storage.Mode {
	return storage.ModeRead | storage.ModeWrite
}

func (r *RcloneStorage) Size() int64 {
	return -1 //TODO: Implement size calculation
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
	payload := map[string]string{
		"fs":     fmt.Sprintf("%s:%s", r.Typee, r.Base),
		"remote": name,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	body, resp := librclone.RPC("operations/list", string(jsonPayload))
	if resp != http.StatusOK {
		return nil, fmt.Errorf("failed to list states: %s", body)
	}

	var response Response
	err = json.Unmarshal([]byte(body), &response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var macs []objects.MAC
	for _, file := range response.List {
		if file.IsDir {
			continue // Skip directories
		}

		mac, err := hex.DecodeString(file.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to create MAC from ID %s: %w", file.ID, err)
		}

		macs = append(macs, objects.MAC(mac))
	}

	return macs, nil
}

func (r *RcloneStorage) GetStates() ([]objects.MAC, error) {
	return r.getMacs("states")
}

func (r *RcloneStorage) PutState(mac objects.MAC, rd io.Reader) (int64, error) {
	return r.putFile(fmt.Sprintf("states/%064x", mac), rd)
}

func (r *RcloneStorage) GetState(mac objects.MAC) (io.Reader, error) {
	return r.getFile(fmt.Sprintf("states/%064x", mac))
}

func (r *RcloneStorage) DeleteState(mac objects.MAC) error {
	return r.deleteFile(fmt.Sprintf("states/%064x", mac))
}

func (r *RcloneStorage) GetPackfiles() ([]objects.MAC, error) {
	return r.getMacs("packfiles")
}

func (r *RcloneStorage) PutPackfile(mac objects.MAC, rd io.Reader) (int64, error) {
	return r.putFile(fmt.Sprintf("packfiles/%064x", mac), rd)
}

func (r *RcloneStorage) GetPackfile(mac objects.MAC) (io.Reader, error) {
	return r.getFile(fmt.Sprintf("packfiles/%064x", mac))
}

func (r *RcloneStorage) GetPackfileBlob(mac objects.MAC, offset uint64, length uint32) (io.Reader, error) {
	rd, err := r.getFile(fmt.Sprintf("packfiles/%064x", mac))
	if err != nil {
		return nil, err
	}

	_, err = rd.Seek(int64(offset), io.SeekStart)
	if err != nil {
		return nil, err
	}
	return io.LimitReader(rd, int64(length)), nil
}

func (r *RcloneStorage) DeletePackfile(mac objects.MAC) error {
	return r.deleteFile(fmt.Sprintf("packfiles/%064x", mac))
}

func (r *RcloneStorage) GetLocks() ([]objects.MAC, error) {
	return r.getMacs("locks")
}

func (r *RcloneStorage) PutLock(lockID objects.MAC, rd io.Reader) (int64, error) {
	return r.putFile(fmt.Sprintf("locks/%064x", lockID), rd)
}

func (r *RcloneStorage) GetLock(lockID objects.MAC) (io.Reader, error) {
	return r.getFile(fmt.Sprintf("locks/%064x", lockID))
}

func (r *RcloneStorage) DeleteLock(lockID objects.MAC) error {
	return r.deleteFile(fmt.Sprintf("locks/%064x", lockID))
}

func (r *RcloneStorage) Close() error {
	utils.DeleteTempConf(r.confFile.Name())
	librclone.Finalize()
	return nil
}
