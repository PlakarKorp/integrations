/*
 * Copyright (c) 2026 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

// Package api is a small client for the Notion public API, version
// 2026-03-11.  It knows about rate limits, retries, pagination, file
// downloads and uploads, and nothing about what plakar does with the
// objects: those are handed back as raw JSON so that the backup keeps
// exactly what the API returned.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://api.notion.com"
	Version        = "2026-03-11"
	PageSize       = 100

	// MaxBody bounds the JSON responses we are willing to decode; the
	// API caps its own payloads well below this.
	MaxBody = 64 << 20

	// MaxListPages bounds a paginated listing: a server handing out
	// endless cursors must not keep us going.
	MaxListPages = 10000

	defaultRate        = 3.0
	defaultConcurrency = 4
	maxAttempts        = 8
	maxBackoff         = 30 * time.Second
	maxRetryAfter      = 5 * time.Minute
	defaultUserAgent   = "plakar-notion/1.0"
)

var (
	ErrMissingToken = errors.New("missing notion token")
	ErrExpiredURL   = errors.New("signed url has expired")
)

// Config sets a Client up.
type Config struct {
	Token       string
	BaseURL     string
	Rate        float64 // requests per second, 0 means 3
	Concurrency int     // in-flight requests, 0 means 4
	UserAgent   string
	HTTPClient  *http.Client
}

// Client talks to one workspace with one token.
type Client struct {
	http      *http.Client
	plain     *http.Client // for signed download urls: no auth header
	base      string
	token     string
	userAgent string
	limiter   *limiter
	sem       chan struct{}
}

// Error is what the API answered with a non-2xx status.
type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("notion: http %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("notion: %s (%d): %s", e.Code, e.Status, e.Message)
}

func apiError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// IsNotFound tells whether the API answered 404: the object does not
// exist or the token cannot see it.
func IsNotFound(err error) bool {
	e := apiError(err)
	return e != nil && e.Status == http.StatusNotFound
}

// IsForbidden tells whether the API answered 403: a missing capability.
func IsForbidden(err error) bool {
	e := apiError(err)
	return e != nil && e.Status == http.StatusForbidden
}

// IsValidation tells whether the API refused the request body.
func IsValidation(err error) bool {
	e := apiError(err)
	return e != nil && e.Status == http.StatusBadRequest
}

// New builds a client; it does not contact the API.
func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, ErrMissingToken
	}
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	} else if base != DefaultBaseURL {
		// a fake API serves the files too
		if u, err := url.Parse(base); err == nil && u.Host != "" {
			RegisterFileHost(u.Host)
		}
	}
	r := cfg.Rate
	if r <= 0 {
		r = defaultRate
	}
	conc := cfg.Concurrency
	if conc <= 0 {
		conc = defaultConcurrency
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	// downloads can be large, so no overall timeout, but a file host
	// that never answers must not hold a reader forever
	plainTransport := http.DefaultTransport.(*http.Transport).Clone()
	if t, ok := hc.Transport.(*http.Transport); ok && t != nil {
		plainTransport = t.Clone()
	}
	plainTransport.ResponseHeaderTimeout = 2 * time.Minute
	plain := &http.Client{
		Transport: plainTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !IsNotionFileURL(req.URL.String()) {
				return fmt.Errorf("redirect to %s refused", req.URL.Host)
			}
			return nil
		},
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	return &Client{
		http:      hc,
		plain:     plain,
		base:      base,
		token:     cfg.Token,
		userAgent: ua,
		limiter:   newLimiter(time.Duration(float64(time.Second) / r)),
		sem:       make(chan struct{}, conc),
	}, nil
}

// limiter spaces requests out and lets a 429 push everybody back.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newLimiter(interval time.Duration) *limiter {
	return &limiter{interval: interval}
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	delay := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	if delay <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *limiter) pause(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	until := time.Now().Add(d)
	if until.After(l.next) {
		l.next = until
	}
}

func (c *Client) acquire(ctx context.Context) (func(), error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := c.limiter.wait(ctx); err != nil {
		<-c.sem
		return nil, err
	}
	return func() { <-c.sem }, nil
}

type request struct {
	method string
	path   string
	query  url.Values
	body   []byte
	// multipart bodies are rebuilt per attempt
	form        func() (io.Reader, string, error)
	idempotent  bool
	maxAttempts int
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func backoff(attempt int) time.Duration {
	d := time.Second << uint(attempt)
	if d > maxBackoff {
		d = maxBackoff
	}
	return d + time.Duration(rand.Int64N(int64(250*time.Millisecond)))
}

// do performs one API request with retries and decodes the JSON answer
// into out when out is not nil.
func (c *Client) do(ctx context.Context, req request, out any) error {
	attempts := req.maxAttempts
	if attempts == 0 {
		attempts = maxAttempts
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		raw, err := c.once(ctx, req)
		if err == nil {
			if out == nil || len(raw) == 0 {
				return nil
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("notion: decoding %s %s: %w", req.method, req.path, err)
			}
			return nil
		}
		last = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e := apiError(err)
		switch {
		case e == nil:
			// transport error: only safe to replay idempotent requests
			if !req.idempotent {
				return err
			}
		case e.Status == http.StatusTooManyRequests || e.Status == 529:
			d := e.RetryAfter
			if d <= 0 {
				d = backoff(attempt)
			}
			if d > maxRetryAfter {
				d = maxRetryAfter
			}
			c.limiter.pause(d)
			if err := sleepCtx(ctx, d); err != nil {
				return err
			}
			continue
		case e.Status == http.StatusConflict:
			if attempt >= 3 {
				return err
			}
		case e.Status >= 500:
			if !req.idempotent {
				return err
			}
		default:
			return err
		}
		if err := sleepCtx(ctx, backoff(attempt)); err != nil {
			return err
		}
	}
	return last
}

func (c *Client) once(ctx context.Context, req request) ([]byte, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	u := c.base + req.path
	if len(req.query) > 0 {
		u += "?" + req.query.Encode()
	}

	var body io.Reader
	contentType := ""
	switch {
	case req.form != nil:
		body, contentType, err = req.form()
		if err != nil {
			return nil, err
		}
	case req.body != nil:
		body = bytes.NewReader(req.body)
		contentType = "application/json"
	}

	hr, err := http.NewRequestWithContext(ctx, req.method, u, body)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.token)
	hr.Header.Set("Notion-Version", Version)
	hr.Header.Set("User-Agent", c.userAgent)
	hr.Header.Set("Accept", "application/json")
	if contentType != "" {
		hr.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("notion: %s %s: %w", req.method, req.path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := readBounded(resp.Body, MaxBody)
	if err != nil {
		return nil, fmt.Errorf("notion: %s %s: %w", req.method, req.path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeError(resp, raw)
	}
	return raw, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("response larger than %d bytes", limit)
	}
	return raw, nil
}

func decodeError(resp *http.Response, raw []byte) error {
	e := &Error{Status: resp.StatusCode}
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil {
		e.Code = body.Code
		e.Message = body.Message
	}
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return e
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, request{method: http.MethodGet, path: path, query: query, idempotent: true}, out)
}

func (c *Client) send(ctx context.Context, method, path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notion: encoding %s %s: %w", method, path, err)
	}
	return c.do(ctx, request{method: method, path: path, body: raw}, out)
}

// RequestStatus tells whether a listing was cut short server side.
type RequestStatus struct {
	Type             string `json:"type"`
	IncompleteReason string `json:"incomplete_reason"`
}

func (s *RequestStatus) Incomplete() bool {
	return s != nil && s.Type == "incomplete"
}

type listPage struct {
	Results       []json.RawMessage `json:"results"`
	NextCursor    *string           `json:"next_cursor"`
	HasMore       bool              `json:"has_more"`
	RequestStatus *RequestStatus    `json:"request_status"`
}

// list walks a paginated endpoint.  GET endpoints take the cursor in the
// query, POST ones in the body.
func (c *Client) list(ctx context.Context, method, path string, query url.Values, body map[string]any, each func(json.RawMessage) error) (*RequestStatus, error) {
	cursor := ""
	for n := 0; ; n++ {
		if n >= MaxListPages {
			return nil, fmt.Errorf("notion: %s %s: more than %d pages of results", method, path, MaxListPages)
		}
		var page listPage
		var err error
		if method == http.MethodGet {
			q := url.Values{}
			for k, v := range query {
				q[k] = v
			}
			q.Set("page_size", strconv.Itoa(PageSize))
			if cursor != "" {
				q.Set("start_cursor", cursor)
			}
			err = c.get(ctx, path, q, &page)
		} else {
			b := map[string]any{}
			for k, v := range body {
				b[k] = v
			}
			b["page_size"] = PageSize
			if cursor != "" {
				b["start_cursor"] = cursor
			}
			raw, merr := json.Marshal(b)
			if merr != nil {
				return nil, fmt.Errorf("notion: encoding %s %s: %w", method, path, merr)
			}
			err = c.do(ctx, request{method: method, path: path, query: query, body: raw, idempotent: true}, &page)
		}
		if err != nil {
			return nil, err
		}
		for _, r := range page.Results {
			if err := each(r); err != nil {
				return nil, err
			}
		}
		if !page.HasMore || page.NextCursor == nil || *page.NextCursor == "" || *page.NextCursor == cursor {
			return page.RequestStatus, nil
		}
		cursor = *page.NextCursor
	}
}

// Me returns the bot user owning the token.
func (c *Client) Me(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.get(ctx, "/v1/users/me", nil, &raw)
}

// Users lists the workspace members.
func (c *Client) Users(ctx context.Context, each func(json.RawMessage) error) error {
	_, err := c.list(ctx, http.MethodGet, "/v1/users", nil, nil, each)
	return err
}

// User retrieves one user.
func (c *Client) User(ctx context.Context, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.get(ctx, "/v1/users/"+url.PathEscape(id), nil, &raw)
}

// Search lists every page or data source ("page" / "data_source") the
// token can see.  The index is eventually consistent, callers must not
// take it for a complete inventory.
func (c *Client) Search(ctx context.Context, object string, each func(json.RawMessage) error) error {
	body := map[string]any{
		"filter": map[string]any{"property": "object", "value": object},
		"sort":   map[string]any{"timestamp": "last_edited_time", "direction": "descending"},
	}
	_, err := c.list(ctx, http.MethodPost, "/v1/search", nil, body, each)
	return err
}

// Page retrieves a page object, without its content.
func (c *Client) Page(ctx context.Context, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.get(ctx, "/v1/pages/"+url.PathEscape(id), nil, &raw)
}

// CreatePage creates a page.
func (c *Client) CreatePage(ctx context.Context, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.send(ctx, http.MethodPost, "/v1/pages", body, &raw)
}

// UpdatePage changes a page's properties, icon, cover or trash status.
func (c *Client) UpdatePage(ctx context.Context, id string, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.send(ctx, http.MethodPatch, "/v1/pages/"+url.PathEscape(id), body, &raw)
}

// PropertyItems pages through a property value the page object truncated
// (relations, people, long titles).
func (c *Client) PropertyItems(ctx context.Context, pageID, propertyID string, each func(json.RawMessage) error) error {
	path := "/v1/pages/" + url.PathEscape(pageID) + "/properties/" + url.PathEscape(propertyID)
	_, err := c.list(ctx, http.MethodGet, path, nil, nil, each)
	return err
}

// Database retrieves a database and the list of its data sources.
func (c *Client) Database(ctx context.Context, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.get(ctx, "/v1/databases/"+url.PathEscape(id), nil, &raw)
}

// CreateDatabase creates a database and its first data source.
func (c *Client) CreateDatabase(ctx context.Context, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.send(ctx, http.MethodPost, "/v1/databases", body, &raw)
}

// UpdateDatabase changes a database's title, icon, cover or trash status.
func (c *Client) UpdateDatabase(ctx context.Context, id string, body any) error {
	var raw json.RawMessage
	return c.send(ctx, http.MethodPatch, "/v1/databases/"+url.PathEscape(id), body, &raw)
}

// DataSource retrieves a data source and its schema.
func (c *Client) DataSource(ctx context.Context, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.get(ctx, "/v1/data_sources/"+url.PathEscape(id), nil, &raw)
}

// CreateDataSource adds a data source to a database.
func (c *Client) CreateDataSource(ctx context.Context, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.send(ctx, http.MethodPost, "/v1/data_sources", body, &raw)
}

// UpdateDataSource changes a data source's schema or title.
func (c *Client) UpdateDataSource(ctx context.Context, id string, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.send(ctx, http.MethodPatch, "/v1/data_sources/"+url.PathEscape(id), body, &raw)
}

// QueryDataSource lists the rows of a data source.  A query stops at
// 10,000 results; when the server reports an incomplete listing the
// query resumes from the creation time of the last row seen.
func (c *Client) QueryDataSource(ctx context.Context, id string, each func(json.RawMessage) error) error {
	path := "/v1/data_sources/" + url.PathEscape(id) + "/query"
	// boundary is the creation time of the newest row seen, seen the
	// rows created at that very time: the only ones a resumed query
	// returns again
	boundary := ""
	seen := map[string]struct{}{}
	for {
		body := map[string]any{
			"sorts": []map[string]any{{"timestamp": "created_time", "direction": "ascending"}},
		}
		if boundary != "" {
			body["filter"] = map[string]any{
				"timestamp":    "created_time",
				"created_time": map[string]any{"on_or_after": boundary},
			}
		}
		n := 0
		status, err := c.list(ctx, http.MethodPost, path, nil, body, func(raw json.RawMessage) error {
			var head struct {
				ID          string `json:"id"`
				CreatedTime string `json:"created_time"`
			}
			if err := json.Unmarshal(raw, &head); err != nil {
				return err
			}
			if head.CreatedTime == boundary {
				if _, dup := seen[head.ID]; dup {
					return nil
				}
			} else if head.CreatedTime > boundary {
				boundary = head.CreatedTime
				seen = map[string]struct{}{}
			}
			seen[head.ID] = struct{}{}
			n++
			return each(raw)
		})
		if err != nil {
			return err
		}
		if !status.Incomplete() || n == 0 || boundary == "" {
			return nil
		}
	}
}

// Block retrieves one block.
func (c *Client) Block(ctx context.Context, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.get(ctx, "/v1/blocks/"+url.PathEscape(id), nil, &raw)
}

// BlockChildren lists the first level of blocks under a page or block.
func (c *Client) BlockChildren(ctx context.Context, id string, each func(json.RawMessage) error) error {
	_, err := c.list(ctx, http.MethodGet, "/v1/blocks/"+url.PathEscape(id)+"/children", nil, nil, each)
	return err
}

// UpdateBlock changes the content of a block.
func (c *Client) UpdateBlock(ctx context.Context, id string, body any) error {
	var raw json.RawMessage
	return c.send(ctx, http.MethodPatch, "/v1/blocks/"+url.PathEscape(id), body, &raw)
}

// DeleteBlock moves a block to the trash.
func (c *Client) DeleteBlock(ctx context.Context, id string) error {
	var raw json.RawMessage
	return c.do(ctx, request{method: http.MethodDelete, path: "/v1/blocks/" + url.PathEscape(id), idempotent: true}, &raw)
}

// AppendChildren adds up to 100 blocks under a page or block and returns
// the created first-level blocks, in order.
func (c *Client) AppendChildren(ctx context.Context, id string, children []any, position any) ([]json.RawMessage, error) {
	body := map[string]any{"children": children}
	if position != nil {
		body["position"] = position
	}
	var page listPage
	if err := c.send(ctx, http.MethodPatch, "/v1/blocks/"+url.PathEscape(id)+"/children", body, &page); err != nil {
		return nil, err
	}
	return page.Results, nil
}

type Markdown struct {
	ID              string   `json:"id"`
	Markdown        string   `json:"markdown"`
	Truncated       bool     `json:"truncated"`
	UnknownBlockIDs []string `json:"unknown_block_ids"`
}

// PageMarkdown renders a page (or a block subtree) as Notion-flavored
// markdown.
func (c *Client) PageMarkdown(ctx context.Context, id string) (*Markdown, error) {
	q := url.Values{"markdown_version": {"v2"}}
	var md Markdown
	if err := c.get(ctx, "/v1/pages/"+url.PathEscape(id)+"/markdown", q, &md); err != nil {
		return nil, err
	}
	return &md, nil
}

// Comments lists the open comments on a page or block.
func (c *Client) Comments(ctx context.Context, blockID string, each func(json.RawMessage) error) error {
	_, err := c.list(ctx, http.MethodGet, "/v1/comments", url.Values{"block_id": {blockID}}, nil, each)
	return err
}

// CreateComment adds a comment or a reply.
func (c *Client) CreateComment(ctx context.Context, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.send(ctx, http.MethodPost, "/v1/comments", body, &raw)
}

// Views lists the ids of a database's views.
func (c *Client) Views(ctx context.Context, databaseID string, each func(json.RawMessage) error) error {
	_, err := c.list(ctx, http.MethodGet, "/v1/views", url.Values{"database_id": {databaseID}}, nil, each)
	return err
}

// View retrieves one view.
func (c *Client) View(ctx context.Context, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.get(ctx, "/v1/views/"+url.PathEscape(id), nil, &raw)
}

// CreateView adds a view to a database.
func (c *Client) CreateView(ctx context.Context, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.send(ctx, http.MethodPost, "/v1/views", body, &raw)
}

// Download fetches a Notion-hosted file through its signed url.  The url
// carries its own credentials, so neither the token nor the API rate
// limit apply.  An expired url yields ErrExpiredURL.
func (c *Client) Download(ctx context.Context, signedURL string) (io.ReadCloser, int64, error) {
	if !IsNotionFileURL(signedURL) {
		return nil, 0, fmt.Errorf("download: %s is not a Notion file host", signedURL)
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoff(attempt-1)); err != nil {
				return nil, 0, err
			}
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
		if err != nil {
			return nil, 0, err
		}
		hr.Header.Set("User-Agent", c.userAgent)
		resp, err := c.plain.Do(hr)
		if err != nil {
			// the url carries its signature: keep it out of errors
			var uerr *url.Error
			if errors.As(err, &uerr) {
				err = uerr.Err
			}
			last = fmt.Errorf("download: %w", err)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return resp.Body, resp.ContentLength, nil
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest:
			_ = resp.Body.Close()
			return nil, 0, ErrExpiredURL
		case resp.StatusCode >= 500:
			_ = resp.Body.Close()
			last = fmt.Errorf("download: http %d", resp.StatusCode)
			continue
		default:
			_ = resp.Body.Close()
			return nil, 0, fmt.Errorf("download: http %d", resp.StatusCode)
		}
	}
	return nil, 0, last
}
