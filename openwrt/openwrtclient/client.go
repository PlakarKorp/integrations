// Package openwrtclient implements communications with OpenWRT device.
package openwrtclient

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type OpenWRTClient struct {
	Login    string
	Password string
	baseURL  string
	session  string
	client   *http.Client
}

type BackupFile struct {
	Filename string
	Size     int
	io.ReadCloser
}

type OpenWRTClientOption func(*OpenWRTClient)

func WithHTTPClient(clt *http.Client) OpenWRTClientOption {
	return func(b *OpenWRTClient) {
		b.client = clt
	}
}

func NewBackupClient(url, login, password string, opts ...OpenWRTClientOption) OpenWRTClient {
	baseURL := strings.TrimSuffix(url, "/")
	clt := OpenWRTClient{
		baseURL:  baseURL,
		Login:    login,
		Password: password,
		client:   http.DefaultClient,
	}

	for _, f := range opts {
		f(&clt)
	}
	return clt
}

func (b *OpenWRTClient) getSession(ctx context.Context) error {
	// var response miniRPCResponse
	var loginRes loginResult

	payload := miniRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "call",
		Params: []any{
			nilSessionID,
			UbusObjectSession,
			UbusMethodLogin,
			map[string]any{
				"username": b.Login,
				"password": b.Password,
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/ubus", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("cannot create login request %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot fetch session id %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("cannot get session %s", resp.Status)
	}

	if err := getResponse(resp.Body, &loginRes); err != nil {
		return err
	}
	b.session = loginRes.Session

	return nil
}

func (b *OpenWRTClient) getArchive(ctx context.Context) (*BackupFile, error) {
	form := url.Values{}
	form.Set("sessionid", b.session)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/cgi-bin/cgi-backup", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("cannot create archive request %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot fetch archive %w", err)
	}
	// We return the body from here let caller Close himself
	if response.StatusCode != 200 {
		_ = response.Body.Close()
		return nil, fmt.Errorf("cannot fetch archive %s", response.Status)
	}

	_, param, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
	if err != nil {
		_ = response.Body.Close()
		return nil, fmt.Errorf("cannot parse Content-Disposition %w", err)
	}

	filename, ok := param["filename"]
	if !ok {
		_ = response.Body.Close()
		return nil, fmt.Errorf("no filename")
	}

	return &BackupFile{
		Filename:   filename,
		Size:       int(response.ContentLength),
		ReadCloser: response.Body,
	}, nil
}

func (b *OpenWRTClient) GetBackupArchive(ctx context.Context) (*BackupFile, error) {
	if b.session == "" {
		if err := b.getSession(ctx); err != nil {
			return nil, err
		}
	}

	return b.getArchive(ctx)
}

type uploadResult struct {
	Size      int64  `json:"size"`
	Checksum  string `json:"checksum"`
	SHA256Sum string `json:"sha256sum"`
}

func (b *OpenWRTClient) UploadArchive(ctx context.Context, archive *bytes.Buffer) error {
	var body bytes.Buffer

	if b.session == "" {
		if err := b.getSession(ctx); err != nil {
			return err
		}
	}
	mw := multipart.NewWriter(&body)

	if err := mw.WriteField("sessionid", b.session); err != nil {
		return err
	}

	if err := mw.WriteField("filename", "/tmp/backup.tar.gz"); err != nil {
		return err
	}

	part, err := mw.CreateFormField("filedata")
	if err != nil {
		return err
	}

	if _, err := part.Write(archive.Bytes()); err != nil {
		return err
	}

	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/cgi-bin/cgi-upload", &body)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("error uploading archive %s", resp.Status)
	}

	var uploaded uploadResult
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		return fmt.Errorf("cannot decode upload response %w", err)
	}

	if uploaded.Size != int64(archive.Len()) {
		return fmt.Errorf("archive size mismatch on device sent %d bytes, device wrote %d", archive.Len(), uploaded.Size)
	}

	if uploaded.SHA256Sum != "" {
		sum := sha256.Sum256(archive.Bytes())
		if !strings.EqualFold(uploaded.SHA256Sum, hex.EncodeToString(sum[:])) {
			return errors.New("archive corrupted sha256sum mismatch")
		}
	} else {
		if uploaded.Checksum == "" {
			return errors.New("no checksum/sha256sum returned, cannot verify integrity")
		}
		sum := md5.Sum(archive.Bytes())
		if !strings.EqualFold(uploaded.Checksum, hex.EncodeToString(sum[:])) {
			return errors.New("archive corrupted checksum mismatch")
		}
	}

	return nil
}

func getResponse(body io.Reader, data any) error {
	var response miniRPCResponse

	if err := json.NewDecoder(body).Decode(&response); err != nil {
		return fmt.Errorf("cannot decode jsonrpc response %w", err)
	}

	if response.Error != nil {
		return errors.New(response.Error.Message)
	}

	if len(response.Result) < 1 {
		return errors.New("missing ubus response code")
	}

	code, err := strconv.Atoi(string(response.Result[0]))
	if err != nil {
		return fmt.Errorf("cannot parse response code %w", err)
	}
	if code != 0 {
		if code == 6 {
			return errors.New("invalid credentials")
		}
		return errors.New("unknown response code")
	}

	if len(response.Result) < 2 {
		return errors.New("missing login payload result")
	}

	if err := json.Unmarshal(response.Result[1], data); err != nil {
		return fmt.Errorf("cannot decode ubus response %w", err)
	}

	return nil
}

func (b *OpenWRTClient) sysupgrade(ctx context.Context) error {
	var response execResult

	payload := miniRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "call",
		Params: []any{
			b.session,
			UbusObjectFile,
			UbusMethodExec,
			map[string]any{
				"command": "/sbin/sysupgrade",
				"params": []string{
					"--restore-backup",
					"/tmp/backup.tar.gz",
				},
			},
		},
	}

	data, err := json.Marshal(&payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/ubus", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return fmt.Errorf("cannot perform sysupgrade %s", resp.Status)
	}

	if err := getResponse(resp.Body, &response); err != nil {
		return err
	}

	if response.Code != 0 {
		return fmt.Errorf("failed to do sysupgrade %s", response.Stderr)
	}

	return nil
}

func (b *OpenWRTClient) Sysupgrade(ctx context.Context) error {
	if b.session == "" {
		err := b.getSession(ctx)
		if err != nil {
			return err
		}
	}
	return b.sysupgrade(ctx)
}

func (b *OpenWRTClient) rebootDevice(ctx context.Context) error {
	var response execResult

	payload := miniRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "call",
		Params: []any{
			b.session,
			UbusObjectFile,
			UbusMethodExec,
			map[string]string{
				"command": "/sbin/reboot",
			},
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/ubus", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return fmt.Errorf("cannot perform reboot %s", resp.Status)
	}

	if err := getResponse(resp.Body, &response); err != nil {
		return err
	}

	if response.Code != 0 {
		return fmt.Errorf("cannot reboot device %s", response.Stderr)
	}

	return nil
}

func (b *OpenWRTClient) RebootDevice(ctx context.Context) error {
	if b.session == "" {
		err := b.getSession(ctx)
		if err != nil {
			return err
		}
	}
	return b.rebootDevice(ctx)
}

func (b *OpenWRTClient) Ping(ctx context.Context) error {
	return b.getSession(ctx)
}
