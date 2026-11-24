// Package xapi is a JSON-RPC client for the XenAPI (XAPI) of an XCP-ng pool
// master. It owns the session: Login opens it, Call adds it to every request,
// and Logout closes it. Typed calls per XAPI class live in package client.
package xapi

import (
	"bytes"
	"context"
	"crypto/tls"
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

type Config struct {
	// URL is the pool master base URL, e.g. https://xcp-master.example.com.
	URL                string
	InsecureSkipVerify bool
	// AllowHTTP permits an http:// URL, which sends credentials in cleartext.
	AllowHTTP bool
	// Timeout applies to each call. Zero means DefaultTimeout.
	Timeout time.Duration
	// MaxResponseSize caps a decoded response body. Zero means
	// DefaultMaxResponseSize.
	MaxResponseSize int64
}

const (
	DefaultTimeout         = time.Minute
	DefaultMaxResponseSize = 64 << 20

	// Originator is sent with every login so our sessions show up as such in
	// the XAPI logs.
	Originator = "plakar"
	apiVersion = "0.1"
)

// Client talks JSON-RPC to one pool master.
type Client struct {
	http            *http.Client
	maxResponseSize int64
	nextID          atomic.Uint64

	mu       sync.Mutex
	base     *url.URL // scheme://host of the pool master
	session  string
	user     string
	password string
}

func New(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.URL)
	if err != nil {
		// *url.Error repeats the URL, credentials included: keep only the cause.
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("xapi: invalid URL: %w", err)
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("xapi: URL must be http(s)://host, got %q", base.Redacted())
	}
	if base.Scheme == "http" && !cfg.AllowHTTP {
		return nil, fmt.Errorf("xapi: http URL would send credentials in cleartext, got %q", base.Redacted())
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	maxResponseSize := cfg.MaxResponseSize
	if maxResponseSize == 0 {
		maxResponseSize = DefaultMaxResponseSize
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in
	}

	return &Client{
		base: &url.URL{Scheme: base.Scheme, Host: base.Host},
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
		},
		maxResponseSize: maxResponseSize,
	}, nil
}

// Login opens a session with session.login_with_password. The credentials
// are kept so Call can open a new session when XAPI drops this one. When the
// configured host is not the pool master, the client switches to the master
// and logs in there.
func (c *Client) Login(ctx context.Context, user, password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.user, c.password = user, password
	return c.login(ctx)
}

// login must be called with c.mu held.
func (c *Client) login(ctx context.Context) error {
	var session string
	err := c.do(ctx, c.base, "session.login_with_password", &session, c.user, c.password, apiVersion, Originator)
	if master, ok := masterOf(err); ok {
		c.base = withHost(c.base, master)
		err = c.do(ctx, c.base, "session.login_with_password", &session, c.user, c.password, apiVersion, Originator)
	}
	if err != nil {
		return err
	}
	c.session = session
	return nil
}

// Logout closes the session. XAPI caps sessions per user and drops the
// oldest ones, so every Login must be paired with a Logout. A session XAPI
// already dropped is not an error.
func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == "" {
		return nil
	}
	err := c.do(ctx, c.base, "session.logout", nil, c.session)
	c.session = ""
	if IsCode(err, CodeSessionInvalid) {
		return nil
	}
	return err
}

// Session returns the current session ref, or "" when not logged in. The
// transfer endpoints (export_raw_vdi, ...) take it as a query parameter.
func (c *Client) Session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// Call invokes method with the session ref prepended to params and decodes
// the result into out, which may be nil. On SESSION_INVALID it logs in again
// and retries once; on HOST_IS_SLAVE it switches to the pool master, logs in
// there and retries once.
func (c *Client) Call(ctx context.Context, method string, out any, params ...any) error {
	base, session := c.state()
	if session == "" {
		return fmt.Errorf("xapi: %s: not logged in", method)
	}

	err := c.do(ctx, base, method, out, prepend(session, params)...)
	xapiErr, ok := errors.AsType[*Error](err)
	if !ok {
		return err
	}
	switch xapiErr.Code {
	case CodeSessionInvalid:
		if err := c.relogin(ctx, session, ""); err != nil {
			return err
		}
	case CodeHostIsSlave:
		master, ok := masterOf(err)
		if !ok {
			return err
		}
		if err := c.relogin(ctx, session, master); err != nil {
			return err
		}
	default:
		return err
	}

	base, session = c.state()
	return c.do(ctx, base, method, out, prepend(session, params)...)
}

func (c *Client) state() (*url.URL, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base, c.session
}

// relogin replaces the session that failed. When master is set the client
// moves there first. Without a master change, a concurrent Call may already
// have replaced stale, in which case there is nothing to do.
func (c *Client) relogin(ctx context.Context, stale, master string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if master == "" && c.session != stale {
		// a stale session code might've already been refreshed
		return nil
	}
	if master != "" {
		c.base = withHost(c.base, master)
	}
	return c.login(ctx)
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      uint64 `json:"id"`
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// do sends one JSON-RPC request. It knows nothing about sessions: callers
// pass the session ref as the first param when the method needs one.
func (c *Client) do(ctx context.Context, base *url.URL, method string, out any, params ...any) error {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(request{JSONRPC: "2.0", Method: method, Params: params, ID: c.nextID.Add(1)})
	if err != nil {
		return fmt.Errorf("xapi: %s: encode: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.JoinPath("jsonrpc").String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("xapi: %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("xapi: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("xapi: %s: HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	// A nil ResponseWriter is fine: it is only used to close server-side
	// connections.
	var res response
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, c.maxResponseSize)).Decode(&res); err != nil {
		return fmt.Errorf("xapi: %s: decode: %w", method, err)
	}
	if res.Error != nil {
		return res.Error.toError(method)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(res.Result, out); err != nil {
		return fmt.Errorf("xapi: %s: decode result: %w", method, err)
	}
	return nil
}

// toError maps a JSON-RPC error to *Error. XAPI puts its error description,
// ["CODE", "param", ...], in data and repeats the code in message.
func (e *rpcError) toError(method string) error {
	var desc []string
	if len(e.Data) > 0 {
		_ = json.Unmarshal(e.Data, &desc)
	}
	if len(desc) == 0 {
		code := e.Message
		if code == "" {
			code = fmt.Sprintf("JSONRPC_%d", e.Code)
		}
		return &Error{Method: method, Code: code}
	}
	return &Error{Method: method, Code: desc[0], Params: desc[1:]}
}

// masterOf returns the pool master address carried by a HOST_IS_SLAVE error.
// the xapi error given is checked on HOST_IS_SLAVE, first parameter should be the master address
func masterOf(err error) (string, bool) {
	xapiErr, ok := errors.AsType[*Error](err)
	if !ok || xapiErr.Code != CodeHostIsSlave || len(xapiErr.Params) == 0 || xapiErr.Params[0] == "" {
		return "", false
	}
	return xapiErr.Params[0], true
}

// withHost returns base pointed at host, keeping the port base was using.
func withHost(base *url.URL, host string) *url.URL {
	u := *base
	switch port := base.Port(); {
	case port != "":
		u.Host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"): // bare IPv6 literal e.g. [::1]
		u.Host = "[" + host + "]"
	default:
		u.Host = host
	}
	return &u
}

func prepend(session string, params []any) []any {
	return append([]any{session}, params...)
}
