package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

func init() {
	storage.Register("oci", 0, New)
}

type ociStore struct {
	client *http.Client
	base   string
	repo   string
}

func New(ctx context.Context, name string, config map[string]string) (storage.Store, error) {
	loc := strings.TrimPrefix(config["location"], "oci://")
	u, err := url.Parse("http://" + loc)
	if err != nil {
		return nil, err
	}

	repo := strings.Trim(u.Path, "/")
	if repo == "" {
		return nil, fmt.Errorf("need a repo")
	}

	u.Path = ""
	base := strings.TrimRight(u.String(), "/")

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	return &ociStore{
		base: base,
		repo: repo,
		client: &http.Client{
			Transport: tr,
			Timeout:   0, // streaming uploads/downloads
		},
	}, nil
}

func (s *ociStore) Create(ctx context.Context, config []byte) error {
	_, err := s.putByTag(ctx, "CONFIG", bytes.NewReader(config))
	return err
}

func (s *ociStore) Open(ctx context.Context) ([]byte, error) {
	rd, err := s.getByTag(ctx, "CONFIG", nil)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(rd)
}

func (s *ociStore) Location(ctx context.Context) (string, error) {
	return strings.Replace(s.base, "http://", "oci://", 1), nil
}

func (s *ociStore) Mode(ctx context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (s *ociStore) Flags() location.Flags {
	return 0
}

func (s *ociStore) Origin() string {
	return s.repo
}

func (s *ociStore) Type() string {
	return "oci"
}

func (s *ociStore) Root() string {
	return s.base
}

func (s *ociStore) Size(ctx context.Context) (int64, error) {
	return -1, nil
}

func (s *ociStore) Close(ctx context.Context) error { return nil }

func (s *ociStore) Ping(ctx context.Context) error { return nil }

func (s *ociStore) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	var prefix string

	switch res {
	case storage.StorageResourcePackfile:
		prefix = "packfiles-"
	case storage.StorageResourceState:
		prefix = "state-"
	case storage.StorageResourceLock:
		prefix = "locks-"
	default:
		return nil, errors.ErrUnsupported
	}
	return s.listByPrefix(ctx, prefix)
}

func (s *ociStore) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	var prefix string

	switch res {
	case storage.StorageResourcePackfile:
		prefix = "packfiles-"
	case storage.StorageResourceState:
		prefix = "state-"
	case storage.StorageResourceLock:
		prefix = "locks-"
	default:
		return -1, errors.ErrUnsupported
	}
	return s.putByTag(ctx, fmt.Sprintf("%s%x", prefix, mac), rd)
}

func (s *ociStore) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	var prefix string

	switch res {
	case storage.StorageResourcePackfile:
		prefix = "packfiles-"
	case storage.StorageResourceState:
		prefix = "state-"
	case storage.StorageResourceLock:
		prefix = "locks-"
	default:
		return nil, errors.ErrUnsupported
	}

	var h http.Header
	if rg != nil {
		end := rg.Offset + uint64(rg.Length) - 1
		h = http.Header{}
		h.Set("Range", fmt.Sprintf("bytes=%d-%d", rg.Offset, end))
	}
	return s.getByTag(ctx, fmt.Sprintf("%s%x", prefix, mac), h)
}

func (s *ociStore) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	var prefix string

	switch res {
	case storage.StorageResourcePackfile:
		prefix = "packfiles-"
	case storage.StorageResourceState:
		prefix = "state-"
	case storage.StorageResourceLock:
		prefix = "locks-"
	default:
		return errors.ErrUnsupported
	}
	return s.deleteByTag(ctx, fmt.Sprintf("%s%x", prefix, mac))
}

// ---- Core: blob upload + manifest(tag) ----

func (s *ociStore) putByTag(ctx context.Context, tag string, rd io.Reader) (int64, error) {
	// stream upload payload blob -> returns digest + size
	payloadDigest, size, err := s.uploadBlob(ctx, rd)
	if err != nil {
		return 0, err
	}

	// upload minimal config blob "{}"
	cfgDigest, _, err := s.uploadBlob(ctx, bytes.NewReader([]byte("{}")))
	if err != nil {
		return 0, err
	}

	// put manifest that references payload blob as a single layer and tag it to chosen "key"
	man := ociManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: descriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    cfgDigest,
			Size:      int64(len("{}")),
		},
		Layers: []descriptor{{
			MediaType: "application/octet-stream",
			Digest:    payloadDigest,
			Size:      size,
		}},
	}

	body, err := json.Marshal(man)
	if err != nil {
		return -1, err
	}

	h := http.Header{}
	h.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	_, err = s.doRepo(ctx, "PUT", "/manifests/"+tag, bytes.NewReader(body), h)
	return size, err
}

func (s *ociStore) getByTag(ctx context.Context, tag string, extraHeaders http.Header) (io.ReadCloser, error) {
	// fetch manifest by tag
	h := http.Header{}
	h.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	manifestRC, resp, err := s.doRepoRC(ctx, "GET", "/manifests/"+tag, nil, h)
	if err != nil {
		return nil, err
	}
	defer manifestRC.Close()

	var man ociManifest
	if err := json.NewDecoder(manifestRC).Decode(&man); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	_ = resp // may be useful for ETag/digest caching later

	if len(man.Layers) < 1 {
		return nil, fmt.Errorf("manifest has no layers")
	}
	layer := man.Layers[0]
	if layer.Digest == "" {
		return nil, fmt.Errorf("manifest layer digest missing")
	}

	// GET blob by digest (optionally ranged)
	h2 := http.Header{}
	for k, vv := range extraHeaders {
		for _, v := range vv {
			h2.Add(k, v)
		}
	}
	return s.doRepoBlobRC(ctx, layer.Digest, h2)
}

func (s *ociStore) deleteByTag(ctx context.Context, tag string) error {
	// Need manifest digest to delete: HEAD /manifests/<tag> gives Docker-Content-Digest
	digest, err := s.headManifestDigest(ctx, tag)
	if err != nil {
		return err
	}
	_, err = s.doRepo(ctx, "DELETE", "/manifests/"+digest, nil, nil)
	return err
}

type tagsList struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

func (s *ociStore) listByPrefix(ctx context.Context, prefix string) ([]objects.MAC, error) {
	// /v2/<name>/tags/list is spec'd but pagination is registry-dependent.
	// good enough to start but will need pagination support.
	rc, _, err := s.doRepoRC(ctx, "GET", "/tags/list", nil, nil)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var tl tagsList
	if err := json.NewDecoder(rc).Decode(&tl); err != nil {
		return nil, err
	}

	var out []objects.MAC
	for _, t := range tl.Tags {
		if strings.HasPrefix(t, prefix) {
			b, err := hex.DecodeString(strings.TrimPrefix(t, prefix))
			if err != nil || len(b) != 32 {
				continue
			}
			var cksum [32]byte
			copy(cksum[:], b[0:32])
			out = append(out, objects.MAC(cksum))
		}
	}
	return out, nil
}

// ---- OCI HTTP primitives ----

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type ociManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

func (s *ociStore) baseURL(p string) string {
	return s.base + "/v2/" + p
}

func (s *ociStore) repoBase() string {
	return s.repo
}

func (s *ociStore) doRepo(ctx context.Context, method, p string, body io.Reader, headers http.Header) (*http.Response, error) {
	_, resp, err := s.do(ctx, method, s.baseURL(s.repoBase()+p), body, headers)
	return resp, err
}

func (s *ociStore) doRepoRC(ctx context.Context, method, p string, body io.Reader, headers http.Header) (io.ReadCloser, *http.Response, error) {
	rc, resp, err := s.do(ctx, method, s.baseURL(s.repoBase()+p), body, headers)
	return rc, resp, err
}

func (s *ociStore) doRepoBlobRC(ctx context.Context, digest string, headers http.Header) (io.ReadCloser, error) {
	rc, _, err := s.do(ctx, "GET", s.baseURL(s.repoBase()+"/blobs/"+digest), nil, headers)
	return rc, err
}

func (s *ociStore) headManifestDigest(ctx context.Context, ref string) (string, error) {
	h := http.Header{}
	h.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	resp, err := s.doRepo(ctx, "HEAD", "/manifests/"+ref, nil, h)
	if err != nil {
		return "", err
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", fmt.Errorf("missing Docker-Content-Digest header on HEAD manifest")
	}
	return d, nil
}
func (s *ociStore) uploadBlob(ctx context.Context, rd io.Reader) (digest string, size int64, err error) {
	// POST start upload
	resp, err := s.doRepo(ctx, "POST", "/blobs/uploads/", nil, nil)
	if err != nil {
		return "", 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", 0, fmt.Errorf("registry missing Location on upload start")
	}
	uploadURL, err := s.resolveLocation(loc)
	if err != nil {
		return "", 0, err
	}

	// PATCH stream + hash
	h := sha256.New()
	tee := io.TeeReader(rd, h)

	patchHeaders := http.Header{}
	patchHeaders.Set("Content-Type", "application/octet-stream")

	rc, resp2, err := s.do(ctx, "PATCH", uploadURL, tee, patchHeaders)
	if err != nil {
		return "", 0, err
	}
	io.Copy(io.Discard, rc)
	rc.Close()

	// IMPORTANT: many registries return an updated Location (updated _state)
	if loc2 := resp2.Header.Get("Location"); loc2 != "" {
		uploadURL, err = s.resolveLocation(loc2)
		if err != nil {
			return "", 0, err
		}
	}

	// size from Range if present (optional)
	size = s.parseUploadedSize(resp2.Header.Get("Range"))

	// Finalize with digest using the *latest* uploadURL
	sum := h.Sum(nil)
	digest = "sha256:" + fmt.Sprintf("%x", sum)

	finalURL := uploadURL
	if strings.Contains(finalURL, "?") {
		finalURL += "&digest=" + url.QueryEscape(digest)
	} else {
		finalURL += "?digest=" + url.QueryEscape(digest)
	}

	rc3, _, err := s.do(ctx, "PUT", finalURL, nil, nil)
	if err != nil {
		return "", 0, err
	}
	io.Copy(io.Discard, rc3)
	rc3.Close()

	return digest, size, nil
}

func (s *ociStore) parseUploadedSize(rng string) int64 {
	// Range is often "0-<lastByte>"
	if rng == "" {
		return 0
	}
	parts := strings.Split(rng, "-")
	if len(parts) != 2 {
		return 0
	}
	var last int64
	_, _ = fmt.Sscanf(parts[1], "%d", &last)
	return last + 1
}

func (s *ociStore) resolveLocation(loc string) (string, error) {
	base, err := url.Parse(strings.TrimRight(s.base, "/")) // or s.location base
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(loc)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

func (s *ociStore) do(ctx context.Context, method, fullURL string, body io.Reader, headers http.Header) (io.ReadCloser, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, nil, err
	}
	if headers != nil {
		for k, vv := range headers {
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
	}
	// Auth
	/*
		if s.cfg.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+s.cfg.BearerToken)
		} else if s.cfg.Username != "" || s.cfg.Password != "" {
			req.SetBasicAuth(s.cfg.Username, s.cfg.Password)
		}
	*/

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, err
	}

	// Minimal status handling
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if resp.Body == nil {
			return io.NopCloser(bytes.NewReader(nil)), resp, nil
		}
		return resp.Body, resp, nil
	}

	// Read small error body for debugging
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return nil, resp, fmt.Errorf("oci %s %s: %s: %s", method, fullURL, resp.Status, strings.TrimSpace(string(b)))
}
