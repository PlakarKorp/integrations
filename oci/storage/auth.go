package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// authenticator puts credentials on requests to the registry host, and
// answers the bearer challenges of token-authenticated registries (Docker
// Hub, GHCR, ECR, Harbor...): the registry names a token realm, the
// credentials go there once, and the token it returns is sent to the
// registry until it expires.
type authenticator struct {
	host      string // the registry: the only host that sees the credentials
	repo      string
	username  string
	password  string
	token     string // static bearer token, used as is
	realmHost string // a token service host other than the registry's, when the user names one
	insecure  bool   // plain-HTTP registry: a plain-HTTP token realm is accepted too
	client    *http.Client

	// basicWanted is set once the registry asks for Basic auth: like docker,
	// the password is not offered before, so a token-authenticated registry
	// never sees it.
	basicWanted atomic.Bool

	mu     sync.Mutex
	tokens map[string]bearerToken // by challenge scope
}

// dockerHubRealms are the token services of the registries that keep theirs
// on another host.
var dockerHubRealms = map[string]string{
	"docker.io":               "auth.docker.io",
	"index.docker.io":         "auth.docker.io",
	"registry-1.docker.io":    "auth.docker.io",
	"registry.hub.docker.com": "auth.docker.io",
}

// maxTokenLifetime caps what a realm's expires_in can make the cache hold.
const maxTokenLifetime = time.Hour

type bearerToken struct {
	value   string
	repo    string // from the scope; "" for a scope-less token
	actions []string
	expires time.Time
}

type bearerChallenge struct {
	realm   string
	service string
	scope   string
}

// expiryMargin is how early a token is dropped, so that a request started
// right before its expiry does not carry a token the registry rejects.
const expiryMargin = 5 * time.Second

func (a *authenticator) forHost(u *url.URL) bool {
	return strings.EqualFold(u.Host, a.host)
}

// authorize puts the best credential at hand on a request to the registry.
func (a *authenticator) authorize(req *http.Request) {
	if !a.forHost(req.URL) {
		return
	}

	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
		return
	}
	if tok, ok := a.cached(req); ok {
		req.Header.Set("Authorization", "Bearer "+tok)
		return
	}
	if a.username != "" && a.basicWanted.Load() {
		req.SetBasicAuth(a.username, a.password)
	}
}

// answerBasic notes a Basic challenge from the registry and says whether a
// retry with the credentials makes sense.
func (a *authenticator) answerBasic() bool {
	if a.username == "" || a.basicWanted.Swap(true) {
		return false
	}
	return true
}

// actionOf is the scope action a request needs, as token-authenticated
// registries grant them.
func actionOf(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead:
		return "pull"
	case http.MethodDelete:
		return "delete"
	default:
		return "push"
	}
}

func (a *authenticator) cached(req *http.Request) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	inRepo := strings.HasPrefix(req.URL.Path, "/v2/"+a.repo+"/")
	action := actionOf(req.Method)

	for scope, tok := range a.tokens {
		if now.After(tok.expires) {
			delete(a.tokens, scope)
			continue
		}
		if inRepo {
			if tok.repo == a.repo && contains(tok.actions, action) {
				return tok.value, true
			}
		} else if tok.repo == "" {
			return tok.value, true
		}
	}
	return "", false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// maxTokens bounds the token cache: a registry that answers every request
// with a new scope must not grow it without end.
const maxTokens = 64

// realmAllowed says whether the credentials may go to a token service: on
// the registry's own host, on the one the user named with token_realm, or
// on Docker Hub's for Docker Hub. The realm is named by the registry, and
// the request to it carries the credentials and comes back as a bearer
// token for the registry, so it must not reach any other host, least of all
// over cleartext.
func (a *authenticator) realmAllowed(realm *url.URL) bool {
	host := strings.ToLower(realm.Hostname())
	switch realm.Scheme {
	case "https":
	case "http":
		if !a.insecure {
			return false
		}
	default:
		return false
	}

	registry := strings.ToLower(hostnameOf(a.host))
	switch host {
	case registry, strings.ToLower(a.realmHost):
		return host != ""
	case dockerHubRealms[registry]:
		return realm.Scheme == "https"
	default:
		return false
	}
}

// exchange fetches a token for a challenge from its realm and caches it.
// The scope must name this repository: the registry gets no token for
// another one out of the user's credentials.
func (a *authenticator) exchange(ctx context.Context, ch bearerChallenge) error {
	realm, err := url.Parse(ch.realm)
	if err != nil || realm.Host == "" || realm.User != nil {
		return fmt.Errorf("invalid token realm %q", ch.realm)
	}
	if !a.realmAllowed(realm) {
		return fmt.Errorf("token realm %q is not on the registry %s (set token_realm to allow it)", ch.realm, a.host)
	}

	for scope := range strings.FieldsSeq(ch.scope) {
		if repo, _ := parseScope(scope); repo != a.repo {
			return fmt.Errorf("registry asks for a token scoped to %q, not to %s", scope, a.repo)
		}
	}

	q := realm.Query()
	if ch.service != "" {
		q.Set("service", ch.service)
	}
	for scope := range strings.FieldsSeq(ch.scope) {
		q.Add("scope", scope)
	}
	realm.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if a.username != "" {
		req.SetBasicAuth(a.username, a.password)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("token exchange at %s: %w", realm.Redacted(), err)
	}
	defer drain(resp)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return &registryError{
			Method:     http.MethodGet,
			URL:        realm.Redacted(),
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(b)),
		}
	}

	value, ttl, err := parseTokenResponse(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return fmt.Errorf("token exchange at %s: %w", realm.Redacted(), err)
	}

	tok := bearerToken{
		value:   value,
		expires: time.Now().Add(ttl - expiryMargin),
	}
	tok.repo, tok.actions = parseScope(ch.scope)

	a.mu.Lock()
	if a.tokens == nil || len(a.tokens) >= maxTokens {
		a.tokens = map[string]bearerToken{}
	}
	a.tokens[ch.scope] = tok
	a.mu.Unlock()
	return nil
}

// parseTokenResponse reads the token of a realm's answer. The keys are
// matched exactly: encoding/json matches field names case-insensitively,
// which would take the "Token" of an instance metadata service's credential
// document for a registry token.
func parseTokenResponse(rd io.Reader) (string, time.Duration, error) {
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(rd).Decode(&fields); err != nil {
		return "", 0, fmt.Errorf("decode: %w", err)
	}

	var value string
	for _, key := range []string{"token", "access_token"} {
		if raw, ok := fields[key]; ok {
			if err := json.Unmarshal(raw, &value); err != nil {
				return "", 0, fmt.Errorf("decode %s: %w", key, err)
			}
			if value != "" {
				break
			}
		}
	}
	if value == "" {
		return "", 0, errors.New("no token in the answer")
	}

	// 60 seconds is the minimum the spec lets a realm hand out.
	var expiresIn int64
	if raw, ok := fields["expires_in"]; ok {
		_ = json.Unmarshal(raw, &expiresIn)
	}
	ttl := time.Duration(expiresIn) * time.Second
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return value, min(ttl, maxTokenLifetime), nil
}

// hostnameOf strips the port of a host[:port].
func hostnameOf(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

// parseScope reads the repository and actions of a "repository:<name>:<a>,<b>"
// scope. Anything else yields no repository, so the token only serves
// requests outside a repository.
func parseScope(scope string) (string, []string) {
	scope, _, _ = strings.Cut(scope, " ")
	parts := strings.Split(scope, ":")
	if len(parts) < 3 || parts[0] != "repository" {
		return "", nil
	}
	repo := strings.Join(parts[1:len(parts)-1], ":")
	return repo, strings.Split(parts[len(parts)-1], ",")
}

// hasBasicChallenge says whether a 401 response asks for Basic auth.
func hasBasicChallenge(h http.Header) bool {
	for _, v := range h.Values("WWW-Authenticate") {
		scheme, _, _ := strings.Cut(strings.TrimSpace(v), " ")
		if strings.EqualFold(scheme, "Basic") {
			return true
		}
	}
	return false
}

// parseBearerChallenge finds the Bearer challenge in a 401 response.
func parseBearerChallenge(h http.Header) (bearerChallenge, bool) {
	for _, v := range h.Values("WWW-Authenticate") {
		scheme, params, _ := strings.Cut(strings.TrimSpace(v), " ")
		if !strings.EqualFold(scheme, "Bearer") {
			continue
		}
		p := parseAuthParams(params)
		if p["realm"] == "" {
			continue
		}
		return bearerChallenge{
			realm:   p["realm"],
			service: p["service"],
			scope:   p["scope"],
		}, true
	}
	return bearerChallenge{}, false
}

// parseAuthParams splits the key="value" list of a challenge; values may
// hold commas when quoted.
func parseAuthParams(s string) map[string]string {
	out := map[string]string{}
	for {
		s = strings.TrimLeft(s, " ,")
		if s == "" {
			return out
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return out
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = s[eq+1:]

		var val string
		if strings.HasPrefix(s, `"`) {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				val, s = s[1:], ""
			} else {
				val, s = s[1:1+end], s[2+end:]
			}
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				val, s = s, ""
			} else {
				val, s = s[:end], s[end+1:]
			}
		}
		out[key] = strings.TrimSpace(val)
	}
}
