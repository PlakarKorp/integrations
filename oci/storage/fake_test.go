package storage_test

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeOptions selects the behaviours of real registries the fake imitates.
type fakeOptions struct {
	// basicUser/basicPass, when set, are the credentials every request must
	// carry, as Basic auth or through a token from the realm.
	basicUser string
	basicPass string
	// tokenAuth answers unauthenticated requests with a Bearer challenge
	// pointing at the fake's /token endpoint, like Docker Hub or GHCR.
	tokenAuth bool
	// realm overrides the realm advertised in the challenge.
	realm string
	// challengeScope overrides the scope advertised in the challenge.
	challengeScope string
	// realmBody overrides the token endpoint's answer.
	realmBody string
	// mirrorChallenge makes the mirror answer every request with a 401 and
	// a Bearer challenge pointing at the registry's token endpoint.
	mirrorChallenge bool
	// staticToken, when set, is the only Authorization the registry takes.
	staticToken string
	// tagDelete allows DELETE /manifests/<tag>. Registries without it only
	// delete by digest, and refuse the tag like the reference registry does,
	// with a 400 DIGEST_INVALID, or with a 405 UNSUPPORTED when untag405 is
	// set.
	tagDelete bool
	untag405  bool
	// ignoreRange serves whole blobs to ranged reads, with a 200.
	ignoreRange bool
	// pageSize caps a tags listing page and links to the next one.
	pageSize int
	// tls serves the registry over TLS.
	tls bool
	// redirectBlobs sends blob downloads to the mirror, like a CDN.
	redirectBlobs bool
	// redirectUploads answers upload PATCHes with a redirect to the mirror.
	redirectUploads bool
}

type request struct {
	method string
	path   string
	auth   string
}

// fake is an in-memory registry speaking the slice of the distribution API
// the connector uses, with the quirks of real ones behind fakeOptions.
type fake struct {
	t      *testing.T
	opts   fakeOptions
	srv    *httptest.Server
	mirror *httptest.Server

	mu        sync.Mutex
	blobs     map[string][]byte
	uploads   map[string][]byte
	manifests map[string][]byte
	tags      map[string]string // tag -> manifest digest
	tokens    map[string]string // token -> scope
	seen      []request
	mirrored  []request
	exchanges int
}

var apiRe = regexp.MustCompile(`^/v2/(.+?)/(blobs/uploads/|blobs/|manifests/|tags/list)(.*)$`)

func newFake(t *testing.T, opts fakeOptions) *fake {
	t.Helper()
	f := &fake{
		t:         t,
		opts:      opts,
		blobs:     map[string][]byte{},
		uploads:   map[string][]byte{},
		manifests: map[string][]byte{},
		tags:      map[string]string{},
		tokens:    map[string]string{},
	}
	if opts.tls {
		f.srv = httptest.NewTLSServer(f)
	} else {
		f.srv = httptest.NewServer(f)
	}
	t.Cleanup(f.srv.Close)

	f.mirror = httptest.NewServer(http.HandlerFunc(f.serveMirror))
	t.Cleanup(f.mirror.Close)
	return f
}

// host is what goes after oci:// in the location.
func (f *fake) host() string {
	return f.srv.Listener.Addr().String()
}

func (f *fake) config(repo string) map[string]string {
	cfg := map[string]string{"location": "oci://" + f.host() + "/" + repo}
	if f.opts.tls {
		cfg["tls_insecure_no_verify"] = "true"
	} else {
		cfg["insecure"] = "true"
	}
	return cfg
}

func (f *fake) requests(method, kind string) []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []request
	for _, r := range f.seen {
		if (method == "" || r.method == method) && strings.Contains(r.path, kind) {
			out = append(out, r)
		}
	}
	return out
}

func (f *fake) mirrorRequests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.mirrored)
}

func (f *fake) tokenExchanges() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges
}

func (f *fake) tagNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.tags))
	for t := range f.tags {
		names = append(names, t)
	}
	slices.Sort(names)
	return names
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, request{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization")})
	f.mu.Unlock()

	switch r.URL.Path {
	case "/token":
		f.serveToken(w, r)
		return
	case "/v2/":
		if f.authorized(w, r, "", "") {
			w.WriteHeader(http.StatusOK)
		}
		return
	}

	m := apiRe.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	repo, kind, rest := m[1], m[2], m[3]

	action := "pull"
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodDelete:
		action = "delete"
	default:
		action = "push"
	}
	if !f.authorized(w, r, repo, action) {
		return
	}

	switch kind {
	case "blobs/uploads/":
		f.serveUpload(w, r, repo, rest)
	case "blobs/":
		f.serveBlob(w, r, rest)
	case "manifests/":
		f.serveManifest(w, r, rest)
	case "tags/list":
		f.serveTags(w, r, repo)
	}
}

func (f *fake) authorized(w http.ResponseWriter, r *http.Request, repo, action string) bool {
	switch {
	case f.opts.staticToken != "":
		if r.Header.Get("Authorization") == "Bearer "+f.opts.staticToken {
			return true
		}
		w.WriteHeader(http.StatusUnauthorized)
		return false

	case f.opts.tokenAuth:
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			f.mu.Lock()
			scope, known := f.tokens[tok]
			f.mu.Unlock()
			if known && scopeAllows(scope, repo, action) {
				return true
			}
		}
		realm := f.opts.realm
		if realm == "" {
			realm = f.srv.URL + "/token"
		}
		challenge := fmt.Sprintf(`Bearer realm=%q,service="fake"`, realm)
		switch {
		case f.opts.challengeScope != "":
			challenge += fmt.Sprintf(`,scope=%q`, f.opts.challengeScope)
		case repo != "":
			actions := action
			if action == "push" {
				actions = "pull,push"
			}
			challenge += fmt.Sprintf(`,scope="repository:%s:%s"`, repo, actions)
		}
		w.Header().Set("WWW-Authenticate", challenge)
		w.WriteHeader(http.StatusUnauthorized)
		return false

	case f.opts.basicUser != "":
		u, p, ok := r.BasicAuth()
		if ok && u == f.opts.basicUser && p == f.opts.basicPass {
			return true
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

func scopeAllows(scope, repo, action string) bool {
	if repo == "" {
		return true
	}
	parts := strings.Split(scope, ":")
	if len(parts) != 3 || parts[0] != "repository" || parts[1] != repo {
		return false
	}
	return slices.Contains(strings.Split(parts[2], ","), action)
}

func (f *fake) serveToken(w http.ResponseWriter, r *http.Request) {
	if f.opts.basicUser != "" {
		u, p, ok := r.BasicAuth()
		if !ok || u != f.opts.basicUser || p != f.opts.basicPass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	tok := randomHex(16)
	f.mu.Lock()
	f.tokens[tok] = r.URL.Query().Get("scope")
	f.exchanges++
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if f.opts.realmBody != "" {
		_, _ = io.WriteString(w, f.opts.realmBody)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_in": 300})
}

func (f *fake) serveUpload(w http.ResponseWriter, r *http.Request, repo, id string) {
	switch {
	case r.Method == http.MethodPost && id == "":
		id := randomHex(8)
		f.mu.Lock()
		f.uploads[id] = nil
		f.mu.Unlock()
		w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/"+id+"?_state=0")
		w.WriteHeader(http.StatusAccepted)

	case r.Method == http.MethodPatch:
		if f.opts.redirectUploads {
			w.Header().Set("Location", f.mirror.URL+"/upload/"+id)
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		buf, ok := f.uploads[id]
		if ok {
			buf = append(buf, body...)
			f.uploads[id] = buf
		}
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/"+id+"?_state="+strconv.Itoa(len(buf)))
		w.Header().Set("Range", "0-"+strconv.Itoa(len(buf)-1))
		w.WriteHeader(http.StatusAccepted)

	case r.Method == http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		digest := r.URL.Query().Get("digest")
		f.mu.Lock()
		defer f.mu.Unlock()
		buf, ok := f.uploads[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		buf = append(buf, body...)
		if digestOf(buf) != digest {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errors":[{"code":"DIGEST_INVALID"}]}`)
			return
		}
		delete(f.uploads, id)
		f.blobs[digest] = buf
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fake) serveBlob(w http.ResponseWriter, r *http.Request, digest string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if f.opts.redirectBlobs && r.Method == http.MethodGet {
		w.Header().Set("Location", f.mirror.URL+"/mirror/"+digest)
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	f.writeBlob(w, r, digest)
}

// serveMirror is the other host blob downloads get redirected to.
func (f *fake) serveMirror(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.mirrored = append(f.mirrored, request{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization")})
	f.mu.Unlock()

	if f.opts.mirrorChallenge {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="mirror"`, f.srv.URL+"/token"))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if digest, ok := strings.CutPrefix(r.URL.Path, "/mirror/"); ok {
		f.writeBlob(w, r, digest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (f *fake) writeBlob(w http.ResponseWriter, r *http.Request, digest string) {
	f.mu.Lock()
	data, ok := f.blobs[digest]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)

	rng := r.Header.Get("Range")
	if rng == "" || f.opts.ignoreRange {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
		return
	}

	var start, end int
	if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil || start < 0 || end < start || end >= len(data) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
	w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
	w.WriteHeader(http.StatusPartialContent)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data[start : end+1])
	}
}

func (f *fake) serveManifest(w http.ResponseWriter, r *http.Request, ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	resolve := func() (string, []byte, bool) {
		digest := ref
		if !strings.HasPrefix(ref, "sha256:") {
			var ok bool
			if digest, ok = f.tags[ref]; !ok {
				return "", nil, false
			}
		}
		body, ok := f.manifests[digest]
		return digest, body, ok
	}

	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var man struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers []struct {
				Digest string `json:"digest"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(body, &man); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errors":[{"code":"MANIFEST_INVALID"}]}`)
			return
		}
		referenced := []string{man.Config.Digest}
		for _, l := range man.Layers {
			referenced = append(referenced, l.Digest)
		}
		for _, d := range referenced {
			if _, ok := f.blobs[d]; !ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"errors":[{"code":"MANIFEST_BLOB_UNKNOWN","detail":"`+d+`"}]}`)
				return
			}
		}
		digest := digestOf(body)
		f.manifests[digest] = body
		f.tags[ref] = digest
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)

	case http.MethodGet, http.MethodHead:
		digest, body, ok := resolve()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}

	case http.MethodDelete:
		if strings.HasPrefix(ref, "sha256:") {
			if _, ok := f.manifests[ref]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(f.manifests, ref)
			for tag, d := range f.tags {
				if d == ref {
					delete(f.tags, tag)
				}
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if !f.opts.tagDelete {
			if f.opts.untag405 {
				w.WriteHeader(http.StatusMethodNotAllowed)
				_, _ = io.WriteString(w, `{"errors":[{"code":"UNSUPPORTED"}]}`)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errors":[{"code":"DIGEST_INVALID","message":"provided digest did not match uploaded content"}]}`)
			return
		}
		if _, ok := f.tags[ref]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.tags, ref)
		w.WriteHeader(http.StatusAccepted)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fake) serveTags(w http.ResponseWriter, r *http.Request, repo string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	all := f.tagNames()
	if last := r.URL.Query().Get("last"); last != "" {
		i, _ := slices.BinarySearch(all, last)
		if i < len(all) && all[i] == last {
			i++
		}
		all = all[i:]
	}

	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if f.opts.pageSize > 0 && (n <= 0 || n > f.opts.pageSize) {
		n = f.opts.pageSize
	}
	page := all
	if n > 0 && len(all) > n {
		page = all[:n]
		w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?n=%d&last=%s>; rel="next"`, repo, n, page[n-1]))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": page})
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
