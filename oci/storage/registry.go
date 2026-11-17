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
	"iter"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/PlakarKorp/integration-sdk"
)

const (
	manifestMediaType       = "application/vnd.oci.image.manifest.v1+json"
	dockerManifestMediaType = "application/vnd.docker.distribution.manifest.v2+json"
	configMediaType         = "application/vnd.oci.image.config.v1+json"
	layerMediaType          = "application/octet-stream"

	// keyAnnotation carries the tag inside its manifest, so that two objects
	// with the same bytes still get distinct manifests: a registry that only
	// deletes by digest drops every tag pointing at the manifest.
	keyAnnotation = "io.plakar.key"

	tagsPageSize    = 1000
	maxErrorBody    = 64 << 10
	maxManifestSize = 4 << 20 // the size the OCI spec asks registries to accept
	maxRedirects    = 10
)

var (
	emptyConfig       = []byte("{}")
	emptyConfigDigest = digestOf(emptyConfig)

	digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type registryOptions struct {
	insecure              bool
	noVerify              bool
	allowCrossHostUploads bool
	username              string
	password              string
	token                 string
}

// registry speaks the slice of the OCI distribution API the store needs,
// against one repository.
type registry struct {
	client                *http.Client
	base                  *url.URL // scheme://host[:port]
	repo                  string
	allowCrossHostUploads bool
	auth                  *authenticator

	emptyConfigUploaded atomic.Bool
}

// registryError is an answer from the registry outside the 2xx range.
type registryError struct {
	Method     string
	URL        string
	StatusCode int
	Status     string
	Body       string
}

func (e *registryError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("oci %s %s: %s", e.Method, e.URL, e.Status)
	}
	return fmt.Sprintf("oci %s %s: %s: %s", e.Method, e.URL, e.Status, e.Body)
}

func hasStatus(err error, code int) bool {
	var re *registryError
	return errors.As(err, &re) && re.StatusCode == code
}

func newRegistry(base *url.URL, repo string, opts registryOptions) (*registry, error) {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || defaultTransport == nil {
		return nil, errors.New("http.DefaultTransport is not a usable *http.Transport")
	}
	tr := defaultTransport.Clone()

	// All requests go to one host, so the per-host idle cap is the one that
	// bites: above it, each finished request drops its connection and the
	// next one redials.
	tr.MaxIdleConnsPerHost = tr.MaxIdleConns

	if !opts.insecure {
		tr.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: opts.noVerify, //nolint:gosec // opt-in, see tls_insecure_no_verify
		}
	}

	r := &registry{
		base:                  base,
		repo:                  repo,
		allowCrossHostUploads: opts.allowCrossHostUploads,
	}
	r.client = &http.Client{
		Transport:     tr,
		CheckRedirect: r.checkRedirect,
		Timeout:       0, // streaming uploads and downloads
	}
	r.auth = &authenticator{
		host:     base.Host,
		repo:     repo,
		username: opts.username,
		password: opts.password,
		token:    opts.token,
		insecure: opts.insecure,
		client:   r.client,
	}
	return r, nil
}

// checkRedirect follows redirects for reads only: blob downloads commonly
// bounce to a CDN or an object store. A redirect never downgrades to
// cleartext, and credentials never leave the registry host.
func (r *registry) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}

	prev := via[len(via)-1]
	switch prev.Method {
	case http.MethodGet, http.MethodHead:
	default:
		// The body is gone or must not go elsewhere: hand back the 3xx,
		// which then fails the status check.
		return http.ErrUseLastResponse
	}

	if req.URL.Scheme != prev.URL.Scheme && req.URL.Scheme != "https" {
		return fmt.Errorf("redirect to %s downgrades the connection", req.URL.Redacted())
	}
	if !strings.EqualFold(req.URL.Host, r.base.Host) {
		req.Header.Del("Authorization")
	}
	return nil
}

func (r *registry) repoURL(p string) string {
	return r.base.String() + "/v2/" + r.repo + p
}

// do sends one request and returns its response when the status is 2xx.
// Any other status is consumed and returned as a *registryError. A bearer
// challenge is answered by a token exchange and one retry.
func (r *registry) do(ctx context.Context, method, u string, body io.Reader, headers http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for k, vv := range headers {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	return r.send(req, true)
}

func (r *registry) send(req *http.Request, answerChallenge bool) (*http.Response, error) {
	r.auth.authorize(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized && answerChallenge {
		if ch, ok := parseBearerChallenge(resp.Header); ok {
			drain(resp)
			if err := r.auth.exchange(req.Context(), ch); err != nil {
				return nil, err
			}
			retry, err := replay(req)
			if err != nil {
				return nil, err
			}
			return r.send(retry, false)
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}

	defer drain(resp)
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return nil, &registryError{
		Method:     req.Method,
		URL:        req.URL.Redacted(),
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(b)),
	}
}

// replay rebuilds a request whose body has been consumed.
func replay(req *http.Request) (*http.Request, error) {
	clone := req.Clone(req.Context())
	clone.Header.Del("Authorization")
	if req.Body == nil || req.Body == http.NoBody {
		return clone, nil
	}
	if req.GetBody == nil {
		return nil, errors.New("registry asked for a token mid-request: the body cannot be sent again")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	clone.Body = body
	return clone, nil
}

// drain discards what is left of a body and closes it, so the connection
// goes back to the pool.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	_ = resp.Body.Close()
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ---- API version ----

func (r *registry) ping(ctx context.Context) error {
	resp, err := r.do(ctx, http.MethodGet, r.base.String()+"/v2/", nil, nil)
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// ---- Manifests ----

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        descriptor        `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

func manifestAccept() http.Header {
	h := http.Header{}
	h.Set("Accept", manifestMediaType+", "+dockerManifestMediaType)
	return h
}

func (r *registry) manifestExists(ctx context.Context, ref string) (bool, error) {
	resp, err := r.do(ctx, http.MethodHead, r.repoURL("/manifests/"+ref), nil, manifestAccept())
	if err != nil {
		if hasStatus(err, http.StatusNotFound) {
			return false, nil
		}
		return false, err
	}
	drain(resp)
	return true, nil
}

func (r *registry) getManifest(ctx context.Context, ref string) (*manifest, error) {
	resp, err := r.do(ctx, http.MethodGet, r.repoURL("/manifests/"+ref), nil, manifestAccept())
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	var man manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxManifestSize)).Decode(&man); err != nil {
		return nil, fmt.Errorf("decode manifest %s: %w", ref, err)
	}
	if len(man.Layers) != 1 {
		return nil, fmt.Errorf("manifest %s has %d layers, want 1", ref, len(man.Layers))
	}
	// The digest goes into a URL: only accept what one looks like.
	if !digestRe.MatchString(man.Layers[0].Digest) {
		return nil, fmt.Errorf("manifest %s has an invalid layer digest %q", ref, man.Layers[0].Digest)
	}
	return &man, nil
}

func (r *registry) manifestDigest(ctx context.Context, ref string) (string, error) {
	resp, err := r.do(ctx, http.MethodHead, r.repoURL("/manifests/"+ref), nil, manifestAccept())
	if err != nil {
		return "", err
	}
	drain(resp)

	d := resp.Header.Get("Docker-Content-Digest")
	if !digestRe.MatchString(d) {
		return "", fmt.Errorf("manifest %s: missing or invalid Docker-Content-Digest header %q", ref, d)
	}
	return d, nil
}

// ---- Objects ----

// putObject streams rd into a blob and tags a manifest pointing at it.
func (r *registry) putObject(ctx context.Context, tag string, rd io.Reader) (int64, error) {
	if err := r.ensureEmptyConfig(ctx); err != nil {
		return -1, err
	}

	payloadDigest, size, err := r.uploadBlob(ctx, rd)
	if err != nil {
		return -1, err
	}

	body, err := json.Marshal(manifest{
		SchemaVersion: 2,
		MediaType:     manifestMediaType,
		Config: descriptor{
			MediaType: configMediaType,
			Digest:    emptyConfigDigest,
			Size:      int64(len(emptyConfig)),
		},
		Layers: []descriptor{{
			MediaType: layerMediaType,
			Digest:    payloadDigest,
			Size:      size,
		}},
		Annotations: map[string]string{keyAnnotation: tag},
	})
	if err != nil {
		return -1, err
	}

	h := http.Header{}
	h.Set("Content-Type", manifestMediaType)
	resp, err := r.do(ctx, http.MethodPut, r.repoURL("/manifests/"+tag), bytes.NewReader(body), h)
	if err != nil {
		return -1, err
	}
	drain(resp)
	return size, nil
}

// ensureEmptyConfig uploads the config blob every manifest shares, once.
func (r *registry) ensureEmptyConfig(ctx context.Context) error {
	if r.emptyConfigUploaded.Load() {
		return nil
	}

	resp, err := r.do(ctx, http.MethodHead, r.repoURL("/blobs/"+emptyConfigDigest), nil, nil)
	if err == nil {
		drain(resp)
		r.emptyConfigUploaded.Store(true)
		return nil
	}
	if !hasStatus(err, http.StatusNotFound) {
		return err
	}

	if _, _, err := r.uploadBlob(ctx, bytes.NewReader(emptyConfig)); err != nil {
		return err
	}
	r.emptyConfigUploaded.Store(true)
	return nil
}

func (r *registry) getObject(ctx context.Context, tag string, rg *integration.Range) (io.ReadCloser, error) {
	man, err := r.getManifest(ctx, tag)
	if err != nil {
		return nil, err
	}

	var h http.Header
	if rg != nil {
		h = http.Header{}
		h.Set("Range", "bytes="+strconv.FormatUint(rg.Offset, 10)+"-"+strconv.FormatUint(rg.Offset+uint64(rg.Length)-1, 10))
	}

	resp, err := r.do(ctx, http.MethodGet, r.repoURL("/blobs/"+man.Layers[0].Digest), nil, h)
	if err != nil {
		return nil, err
	}
	if rg == nil {
		return resp.Body, nil
	}

	switch resp.StatusCode {
	case http.StatusPartialContent:
		return &limitedBody{Reader: io.LimitReader(resp.Body, int64(rg.Length)), body: resp.Body}, nil
	case http.StatusOK:
		// The registry, or whatever it redirected to, ignored the Range:
		// cut the window out of the whole blob.
		if _, err := io.CopyN(io.Discard, resp.Body, int64(rg.Offset)); err != nil {
			drain(resp)
			return nil, fmt.Errorf("skip to offset %d: %w", rg.Offset, err)
		}
		return &limitedBody{Reader: io.LimitReader(resp.Body, int64(rg.Length)), body: resp.Body}, nil
	default:
		drain(resp)
		return nil, fmt.Errorf("oci GET blob %s: unexpected %s for a ranged read", man.Layers[0].Digest, resp.Status)
	}
}

// limitedBody reads a window of a response body and closes the whole body.
type limitedBody struct {
	io.Reader
	body io.ReadCloser
}

func (b *limitedBody) Close() error {
	return b.body.Close()
}

// deleteObject removes the tag, and with it the manifest. Registries that
// cannot untag (the reference one among them) delete by digest; every
// manifest is unique to its tag (see keyAnnotation), so that takes no other
// tag with it.
func (r *registry) deleteObject(ctx context.Context, tag string) error {
	resp, err := r.do(ctx, http.MethodDelete, r.repoURL("/manifests/"+tag), nil, nil)
	if err == nil {
		drain(resp)
		return nil
	}
	if !tagDeleteUnsupported(err) {
		return err
	}

	digest, err := r.manifestDigest(ctx, tag)
	if err != nil {
		return err
	}
	resp, err = r.do(ctx, http.MethodDelete, r.repoURL("/manifests/"+digest), nil, nil)
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

func tagDeleteUnsupported(err error) bool {
	var re *registryError
	if !errors.As(err, &re) {
		return false
	}
	switch re.StatusCode {
	case http.StatusMethodNotAllowed:
		return true
	case http.StatusBadRequest:
		// The reference registry reads the tag as a digest and complains
		// about that digest.
		return strings.Contains(re.Body, "UNSUPPORTED") || strings.Contains(re.Body, "DIGEST_INVALID")
	default:
		return false
	}
}

// ---- Tags ----

type tagsList struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// tags lists every tag of the repository, following the pagination links.
func (r *registry) tags(ctx context.Context) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		next := r.repoURL("/tags/list?n=" + strconv.Itoa(tagsPageSize))
		seen := map[string]bool{}

		for next != "" {
			if seen[next] {
				yield("", fmt.Errorf("tags listing loops back to %s", next))
				return
			}
			seen[next] = true

			h := http.Header{}
			h.Set("Accept", "application/json")
			resp, err := r.do(ctx, http.MethodGet, next, nil, h)
			if err != nil {
				// A repository nobody pushed to yet has no tags.
				if !hasStatus(err, http.StatusNotFound) {
					yield("", err)
				}
				return
			}

			var tl tagsList
			err = json.NewDecoder(resp.Body).Decode(&tl)
			drain(resp)
			if err != nil {
				yield("", fmt.Errorf("decode tags list: %w", err))
				return
			}

			for _, t := range tl.Tags {
				if !yield(t, nil) {
					return
				}
			}

			next, err = r.nextPage(resp.Header)
			if err != nil {
				yield("", err)
				return
			}
		}
	}
}

// nextPage returns the URL of the next tags page, from the Link header, or
// "" on the last page.
func (r *registry) nextPage(h http.Header) (string, error) {
	for _, link := range h.Values("Link") {
		for part := range strings.SplitSeq(link, ",") {
			target, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			target = strings.TrimSpace(target)
			if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
				continue
			}
			if !isNextRel(params) {
				continue
			}

			ref, err := url.Parse(target[1 : len(target)-1])
			if err != nil {
				return "", fmt.Errorf("invalid Link header %q: %w", link, err)
			}
			resolved := r.base.ResolveReference(ref)
			if resolved.Scheme != r.base.Scheme || !strings.EqualFold(resolved.Host, r.base.Host) {
				return "", fmt.Errorf("tags listing continues off the registry, at %s", resolved.Redacted())
			}
			return resolved.String(), nil
		}
	}
	return "", nil
}

func isNextRel(params string) bool {
	for p := range strings.SplitSeq(params, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		if strings.EqualFold(strings.TrimSpace(k), "rel") && strings.Trim(strings.TrimSpace(v), `"`) == "next" {
			return true
		}
	}
	return false
}

// ---- Blobs ----

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// uploadBlob streams rd into a new blob and returns its digest and size.
func (r *registry) uploadBlob(ctx context.Context, rd io.Reader) (string, int64, error) {
	resp, err := r.do(ctx, http.MethodPost, r.repoURL("/blobs/uploads/"), nil, nil)
	if err != nil {
		return "", 0, err
	}
	drain(resp)

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", 0, errors.New("registry missing Location on upload start")
	}
	uploadURL, err := r.resolveUploadLocation(loc)
	if err != nil {
		return "", 0, err
	}

	h := sha256.New()
	cr := &countingReader{r: io.TeeReader(rd, h)}

	patchHeaders := http.Header{}
	patchHeaders.Set("Content-Type", layerMediaType)
	resp, err = r.do(ctx, http.MethodPatch, uploadURL, cr, patchHeaders)
	if err != nil {
		return "", 0, err
	}
	drain(resp)

	// Many registries hand back an updated Location (a new _state).
	if loc := resp.Header.Get("Location"); loc != "" {
		uploadURL, err = r.resolveUploadLocation(loc)
		if err != nil {
			return "", 0, err
		}
	}

	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))

	final, err := url.Parse(uploadURL)
	if err != nil {
		return "", 0, err
	}
	q := final.Query()
	q.Set("digest", digest)
	final.RawQuery = q.Encode()

	resp, err = r.do(ctx, http.MethodPut, final.String(), nil, nil)
	if err != nil {
		return "", 0, err
	}
	drain(resp)

	return digest, cr.n, nil
}

// resolveUploadLocation turns an upload Location into an absolute URL. The
// upload body follows it, so it stays on the registry unless told otherwise,
// and never falls back to cleartext.
func (r *registry) resolveUploadLocation(loc string) (string, error) {
	ref, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("invalid upload location %q: %w", loc, err)
	}
	resolved := r.base.ResolveReference(ref)
	if resolved.Scheme != r.base.Scheme {
		return "", fmt.Errorf("registry upload location %q is not %s", loc, r.base.Scheme)
	}
	if !r.allowCrossHostUploads && !strings.EqualFold(resolved.Host, r.base.Host) {
		return "", fmt.Errorf("registry upload location %q is not on %s", loc, r.base.Host)
	}
	return resolved.String(), nil
}
