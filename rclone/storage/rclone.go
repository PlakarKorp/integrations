package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/PlakarKorp/integration-rclone/utils"

	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/storage"
	"github.com/rclone/rclone/librclone/librclone"
)

type RcloneStorage struct {
	Typee    string
	Base     string
	confFile *os.File
}

func NewRcloneStorage(ctx context.Context, name string, config map[string]string) (storage.Store, error) {
	protocol, base, found := strings.Cut(config["location"], ":")
	if !found {
		return nil, fmt.Errorf("invalid location: %s. Expected format: remote:path/to/dir", name+"://"+config["location"])
	}

	file, err := utils.WriteRcloneConfigFile(protocol, config)
	if err != nil {
		return nil, err
	}

	typee, found := config["type"]
	if !found {
		return nil, fmt.Errorf("missing type in configuration for %s", name)
	}

	librclone.Initialize()

	return &RcloneStorage{
		Typee:    typee,
		Base:     base,
		confFile: file,
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

func (r *RcloneStorage) newFile(rd io.Reader) error {
	tmpFile, err := os.CreateTemp("", "tempfile-*.tmp")
	if err != nil {
		return err
	}
	defer tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	_, err = io.Copy(tmpFile, rd)
	if err != nil {
		return err
	}

	payload := map[string]string{
		"srcFs":     "/",
		"srcRemote": tmpFile.Name(),
		"dstFs":     fmt.Sprintf("%s:%s", r.Typee, r.Base),
		"dstRemote": "", //TODO define the destination remote path
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

func (r *RcloneStorage) Create(ctx context.Context, config []byte) error {

	err := r.newFile(bytes.NewReader(config))
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
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) Location() string {
	return fmt.Sprintf("%s://%s", r.Typee, r.Base)
}

func (r *RcloneStorage) Mode() storage.Mode {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) Size() int64 {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) GetStates() ([]objects.MAC, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) PutState(mac objects.MAC, rd io.Reader) (int64, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) GetState(mac objects.MAC) (io.Reader, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) DeleteState(mac objects.MAC) error {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) GetPackfiles() ([]objects.MAC, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) PutPackfile(mac objects.MAC, rd io.Reader) (int64, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) GetPackfile(mac objects.MAC) (io.Reader, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) GetPackfileBlob(mac objects.MAC, offset uint64, length uint32) (io.Reader, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) DeletePackfile(mac objects.MAC) error {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) GetLocks() ([]objects.MAC, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) PutLock(lockID objects.MAC, rd io.Reader) (int64, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) GetLock(lockID objects.MAC) (io.Reader, error) {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) DeleteLock(lockID objects.MAC) error {
	//TODO implement me
	panic("implement me")
}

func (r *RcloneStorage) Close() error {
	//TODO implement me
	panic("implement me")
}
