// Package api is the subset of the Notion REST API the importer and the
// exporter need: discovery and reads for backups, creation for restores.
//
// Every call goes through one http.Client with a timeout, honours the
// context, retries rate limits and transient server errors, and reads
// responses through a size cap, so a slow, hostile or hung endpoint cannot
// stall or exhaust a backup.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the public Notion API.
	DefaultBaseURL = "https://api.notion.com/v1"

	// Version is the Notion-Version header sent with every request.
	//
	// 2025-09-03 moved database schemas and queries to data sources; this
	// code speaks the previous contract, which Notion still serves. See the
	// README for what that means for multi-source databases.
	Version = "2022-06-28"

	// PageSize is the largest page Notion hands out on list endpoints.
	PageSize = 100

	// maxJSONBody caps a JSON response. A page or a page of blocks is at
	// most a few megabytes; anything near this is not a Notion answer.
	maxJSONBody = 64 << 20

	// MaxSinglePartUpload is the size limit of a single-part file upload.
	MaxSinglePartUpload = 20 << 20

	maxAttempts   = 6
	maxRetryDelay = 30 * time.Second
	maxRedirects  = 10
)

// Error is a non-2xx answer from Notion.
type Error struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("notion: http %d", e.Status)
	}
	return fmt.Sprintf("notion: %s (%d): %s", e.Code, e.Status, e.Message)
}

// IsStatus reports whether err is a Notion error with the given HTTP status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// Client talks to one Notion workspace with one integration token.
type Client struct {
	token   string
	baseURL string
	// http makes the API calls. Their bodies are small and read whole,
	// so a deadline on the entire exchange is right.
	http *http.Client
	// files fetches attachments, whose bodies are streamed into the
	// store for as long as they take: it has deadlines on the dial and
	// the headers only, the context bounds the rest. It also re-checks
	// the destination on every redirect, see FetchAttachment.
	files *http.Client
	// insecure lifts the attachment guard. It is set when the API itself
	// is reached over plain http, which only a test server is.
	insecure bool
	// sleep is swapped by tests so retries do not wait for real.
	sleep func(context.Context, time.Duration) error
}

// NewClient returns a client for the API at baseURL, DefaultBaseURL when
// empty.
func NewClient(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	const timeout = 2 * time.Minute
	c := &Client{
		token:    token,
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		http:     &http.Client{Timeout: timeout},
		insecure: strings.HasPrefix(baseURL, "http://"),
		sleep:    sleepCtx,
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout
	c.files = &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			return c.checkAttachmentURL(req.URL)
		},
	}
	return c
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do sends one JSON request and decodes the answer into out when out is not
// nil.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	contentType := ""
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		contentType = "application/json"
	}
	return c.request(ctx, method, path, contentType, payload, out)
}

// request sends payload and decodes the JSON answer into out when out is
// not nil. It retries 429 and 5xx answers with the delay Notion asks for,
// or an exponential backoff when it does not say.
//
// Writes are retried too. A 5xx may mean the write landed, and a retry
// then duplicates a block or a page; a restore that stops on the first
// hiccup loses more, and whatever happens is reported on the record.
func (c *Client) request(ctx context.Context, method, path, contentType string, payload []byte, out any) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, retryDelay(lastErr, attempt)); err != nil {
				return err
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("%s %s: %w", method, path, err)
		}
		c.authorize(req)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("%s %s: %w", method, path, err)
			if isTemporary(err) {
				continue
			}
			return lastErr
		}

		data, err := readBounded(resp.Body, maxJSONBody)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("%s %s: %w", method, path, err)
		}

		if resp.StatusCode/100 != 2 {
			apiErr := &Error{Status: resp.StatusCode}
			_ = json.Unmarshal(data, apiErr)
			lastErr = apiErr
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				if ra := retryAfter(resp); ra > 0 {
					lastErr = &retryAfterError{err: apiErr, after: ra}
				}
				continue
			}
			return fmt.Errorf("%s %s: %w", method, path, apiErr)
		}

		if out == nil {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: decode answer: %w", method, path, err)
		}
		return nil
	}

	var rae *retryAfterError
	if errors.As(lastErr, &rae) {
		lastErr = rae.err
	}
	return fmt.Errorf("%s %s: giving up after %d attempts: %w", method, path, maxAttempts, lastErr)
}

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", Version)
}

// retryAfterError carries the delay Notion asked for alongside the answer.
type retryAfterError struct {
	err   *Error
	after time.Duration
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }

func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

func retryDelay(err error, attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
	var rae *retryAfterError
	if errors.As(err, &rae) && rae.after > d {
		d = rae.after
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	return d
}

func isTemporary(err error) bool {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// readBounded reads at most limit bytes and fails, rather than truncates,
// when the body is longer.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response larger than %d bytes", limit)
	}
	return data, nil
}

// Me is the integration the token belongs to.
type Me struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Bot  struct {
		WorkspaceName string `json:"workspace_name"`
	} `json:"bot"`
}

// Me asks who the token belongs to, which also checks the token.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var out Me
	if err := c.do(ctx, http.MethodGet, "/users/me", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Ping checks the token.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Me(ctx)
	return err
}

// Search lists every page and database shared with the integration, one
// page of results at a time. An empty cursor starts from the beginning.
func (c *Client) Search(ctx context.Context, cursor string) (*List, error) {
	body := map[string]any{"page_size": PageSize}
	if cursor != "" {
		body["start_cursor"] = cursor
	}
	var out List
	if err := c.do(ctx, http.MethodPost, "/search", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SearchAll walks every page of Search.
func (c *Client) SearchAll(ctx context.Context) ([]Object, error) {
	return c.collect(ctx, c.Search)
}

// Page fetches one page.
func (c *Client) Page(ctx context.Context, id string) (*Object, error) {
	return c.object(ctx, "/pages/"+url.PathEscape(id))
}

// Database fetches one database, that is its schema and metadata.
func (c *Client) Database(ctx context.Context, id string) (*Object, error) {
	return c.object(ctx, "/databases/"+url.PathEscape(id))
}

// Block fetches one block.
func (c *Client) Block(ctx context.Context, id string) (*Object, error) {
	return c.object(ctx, "/blocks/"+url.PathEscape(id))
}

func (c *Client) object(ctx context.Context, path string) (*Object, error) {
	var out Object
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BlockChildren lists the direct children of a block or page.
func (c *Client) BlockChildren(ctx context.Context, id, cursor string) (*List, error) {
	q := url.Values{"page_size": {strconv.Itoa(PageSize)}}
	if cursor != "" {
		q.Set("start_cursor", cursor)
	}
	var out List
	if err := c.do(ctx, http.MethodGet, "/blocks/"+url.PathEscape(id)+"/children?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AllBlockChildren walks every page of BlockChildren.
func (c *Client) AllBlockChildren(ctx context.Context, id string) ([]Object, error) {
	return c.collect(ctx, func(ctx context.Context, cursor string) (*List, error) {
		return c.BlockChildren(ctx, id, cursor)
	})
}

// QueryDatabase lists the pages of a database.
func (c *Client) QueryDatabase(ctx context.Context, id, cursor string) (*List, error) {
	body := map[string]any{"page_size": PageSize}
	if cursor != "" {
		body["start_cursor"] = cursor
	}
	var out List
	if err := c.do(ctx, http.MethodPost, "/databases/"+url.PathEscape(id)+"/query", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AllDatabasePages walks every page of QueryDatabase.
func (c *Client) AllDatabasePages(ctx context.Context, id string) ([]Object, error) {
	return c.collect(ctx, func(ctx context.Context, cursor string) (*List, error) {
		return c.QueryDatabase(ctx, id, cursor)
	})
}

func (c *Client) collect(ctx context.Context, fetch func(context.Context, string) (*List, error)) ([]Object, error) {
	var all []Object
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := fetch(ctx, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if !page.HasMore || page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// CreatePage creates a page from a body already shaped for the API.
func (c *Client) CreatePage(ctx context.Context, body map[string]any) (*Object, error) {
	var out Object
	if err := c.do(ctx, http.MethodPost, "/pages", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateDatabase creates a database from a body already shaped for the API.
func (c *Client) CreateDatabase(ctx context.Context, body map[string]any) (*Object, error) {
	var out Object
	if err := c.do(ctx, http.MethodPost, "/databases", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppendChildren appends blocks to a block or page and returns the created
// blocks in order. Notion takes at most PageSize blocks per call.
func (c *Client) AppendChildren(ctx context.Context, parentID string, children []map[string]any) ([]Object, error) {
	if len(children) > PageSize {
		return nil, fmt.Errorf("append children: %d blocks, at most %d per call", len(children), PageSize)
	}
	var out List
	body := map[string]any{"children": children}
	if err := c.do(ctx, http.MethodPatch, "/blocks/"+url.PathEscape(parentID)+"/children", body, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// UploadFile pushes a file of at most MaxSinglePartUpload bytes and returns
// the upload id a block, icon or cover can then reference.
func (c *Client) UploadFile(ctx context.Context, filename, contentType string, r io.Reader) (string, error) {
	data, err := readBounded(r, MaxSinglePartUpload)
	if err != nil {
		return "", fmt.Errorf("upload %s: %w", filename, err)
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}

	var created struct {
		ID string `json:"id"`
	}
	body := map[string]any{"mode": "single_part", "filename": filename, "content_type": contentType}
	if err := c.do(ctx, http.MethodPost, "/file_uploads", body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("upload: no id in answer")
	}

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("upload %s: %w", filename, err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("upload %s: %w", filename, err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("upload %s: %w", filename, err)
	}

	path := "/file_uploads/" + url.PathEscape(created.ID) + "/send"
	if err := c.request(ctx, http.MethodPost, path, mw.FormDataContentType(), form.Bytes(), nil); err != nil {
		return "", fmt.Errorf("upload %s: %w", filename, err)
	}
	return created.ID, nil
}

// FetchAttachment opens a file Notion pointed us at.
//
// The URL comes out of an API answer, so it was chosen by whoever can add a
// block to the workspace, not by us. It must be https and every hop is
// checked again so a redirect cannot walk it towards an internal address.
// The caller closes the body.
func (c *Client) FetchAttachment(ctx context.Context, rawurl string) (*http.Response, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, fmt.Errorf("attachment url: %w", err)
	}
	if err := c.checkAttachmentURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("attachment: %w", err)
	}
	resp, err := c.files.Do(req)
	if err != nil {
		return nil, fmt.Errorf("attachment: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, &Error{Status: resp.StatusCode, Code: "attachment", Message: "fetch " + u.Host + u.Path}
	}
	return resp, nil
}

func (c *Client) checkAttachmentURL(u *url.URL) error {
	if c.insecure {
		return nil
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing to fetch attachment over %q, https required", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("attachment url has no host")
	}
	// Only literal addresses can be checked without resolving; a name that
	// resolves to a private address is not caught here, which is why this
	// is a guard rail rather than a boundary.
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() {
			return fmt.Errorf("refusing to fetch attachment from non-public address %q", host)
		}
	}
	return nil
}
