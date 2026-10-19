package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/notion/internal/fakenotion"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c := NewClient(fakenotion.Token, baseURL)
	c.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return c
}

func TestClientReadsAndPaginates(t *testing.T) {
	srv := fakenotion.New(t)
	srv.PageSize = 2
	root := srv.AddPage("workspace", "", "Root", nil)
	var blocks []string
	for i := 0; i < 5; i++ {
		blocks = append(blocks, srv.AddBlock(root, "paragraph", map[string]any{"rich_text": fakenotion.RichText("p")}))
	}
	db := srv.AddDatabase(root, "Tasks", nil)
	for i := 0; i < 3; i++ {
		srv.AddPage("database_id", db, "row", nil)
	}

	c := newTestClient(t, srv.URL())
	ctx := context.Background()

	require.NoError(t, c.Ping(ctx))
	me, err := c.Me(ctx)
	require.NoError(t, err)
	require.Equal(t, fakenotion.WorkspaceName, me.Bot.WorkspaceName)

	page, err := c.Page(ctx, root)
	require.NoError(t, err)
	require.Equal(t, "page", page.Object)
	require.Equal(t, "Root", page.Title())
	require.Equal(t, "workspace", page.Parent.Type)
	require.Contains(t, string(page.Raw), `"properties"`)

	children, err := c.AllBlockChildren(ctx, root)
	require.NoError(t, err)
	// 5 paragraphs plus the child_database block.
	require.Len(t, children, 6)
	for i, id := range blocks {
		require.Equal(t, id, children[i].ID)
	}

	all, err := c.SearchAll(ctx)
	require.NoError(t, err)
	require.Len(t, all, 5) // root, database, 3 rows

	rows, err := c.AllDatabasePages(ctx, db)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, db, rows[0].Parent.ID())

	d, err := c.Database(ctx, db)
	require.NoError(t, err)
	require.Equal(t, "Tasks", d.Title())
}

func TestClientRetriesRateLimit(t *testing.T) {
	srv := fakenotion.New(t)
	root := srv.AddPage("workspace", "", "Root", nil)
	c := newTestClient(t, srv.URL())

	srv.RateLimitNext(2)
	_, err := c.Page(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, 3, srv.Requests())

	srv.RateLimitNext(maxAttempts + 1)
	_, err = c.Page(context.Background(), root)
	require.Error(t, err)
	require.True(t, IsStatus(err, http.StatusTooManyRequests), err)
}

func TestClientRetriesServerErrors(t *testing.T) {
	srv := fakenotion.New(t)
	root := srv.AddPage("workspace", "", "Root", nil)
	c := newTestClient(t, srv.URL())

	srv.ServerErrorNext(2)
	_, err := c.Page(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, 3, srv.Requests())

	srv.ServerErrorNext(maxAttempts)
	_, err = c.Page(context.Background(), root)
	require.True(t, IsStatus(err, http.StatusServiceUnavailable), err)
}

func TestClientDoesNotRetryClientErrors(t *testing.T) {
	srv := fakenotion.New(t)
	c := newTestClient(t, srv.URL())

	_, err := c.Page(context.Background(), "00000000-0000-0000-0000-000000000000")
	require.True(t, IsStatus(err, http.StatusNotFound), err)
	require.Equal(t, 1, srv.Requests())

	var apiErr *Error
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, "object_not_found", apiErr.Code)
}

func TestClientHonoursContext(t *testing.T) {
	srv := fakenotion.New(t)
	root := srv.AddPage("workspace", "", "Root", nil)
	c := newTestClient(t, srv.URL())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Page(ctx, root)
	require.ErrorIs(t, err, context.Canceled)
}

func TestClientRefusesOversizedAnswer(t *testing.T) {
	var served atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"page","id":"x","padding":"`))
		_, _ = io.Copy(w, io.LimitReader(zeros{}, maxJSONBody+1))
		_, _ = w.Write([]byte(`"}`))
	}))
	t.Cleanup(ts.Close)

	c := newTestClient(t, ts.URL)
	_, err := c.Page(context.Background(), "x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "larger than")
	require.Equal(t, int32(1), served.Load())
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = '0'
	}
	return len(p), nil
}

func TestAppendChildrenLimit(t *testing.T) {
	srv := fakenotion.New(t)
	c := newTestClient(t, srv.URL())
	children := make([]map[string]any, PageSize+1)
	_, err := c.AppendChildren(context.Background(), "x", children)
	require.Error(t, err)
	require.Equal(t, 0, srv.Requests())
}

func TestUploadFile(t *testing.T) {
	srv := fakenotion.New(t)
	c := newTestClient(t, srv.URL())

	id, err := c.UploadFile(context.Background(), "pic.png", "image/png", strings.NewReader("png-bytes"))
	require.NoError(t, err)
	require.Equal(t, []byte("png-bytes"), srv.Upload(id))

	// The send step is rate limited like any other call.
	srv.RateLimitPath("/send", 2)
	id, err = c.UploadFile(context.Background(), "pic2.png", "image/png", strings.NewReader("more"))
	require.NoError(t, err)
	require.Equal(t, []byte("more"), srv.Upload(id))

	_, err = c.UploadFile(context.Background(), "big.bin", "", io.LimitReader(zeros{}, MaxSinglePartUpload+1))
	require.Error(t, err)
	require.Contains(t, err.Error(), "larger than")
}

func TestFetchAttachmentGuards(t *testing.T) {
	c := newTestClient(t, "https://api.example.invalid/v1")
	for _, u := range []string{
		"http://example.com/img.png",
		"file:///etc/passwd",
		"https://127.0.0.1/admin",
		"https://169.254.169.254/meta",
		"https://10.0.0.1/internal",
		"https://[::1]/x",
		"https:///nohost",
	} {
		_, err := c.FetchAttachment(context.Background(), u)
		require.Error(t, err, u)
	}
}

func TestFetchAttachmentFollowsAndChecksRedirects(t *testing.T) {
	var hops atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			hops.Add(1)
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			_, _ = w.Write([]byte("payload"))
		case "/loop":
			hops.Add(1)
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	c := newTestClient(t, ts.URL)
	resp, err := c.FetchAttachment(context.Background(), ts.URL+"/start")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, "payload", string(body))

	_, err = c.FetchAttachment(context.Background(), ts.URL+"/loop")
	require.Error(t, err)
	require.LessOrEqual(t, hops.Load(), int32(maxRedirects+1))

	_, err = c.FetchAttachment(context.Background(), ts.URL+"/missing")
	require.True(t, IsStatus(err, http.StatusNotFound), err)
}

func TestRetryDelay(t *testing.T) {
	require.Equal(t, 500*time.Millisecond, retryDelay(nil, 1))
	require.Equal(t, time.Second, retryDelay(nil, 2))
	require.Equal(t, maxRetryDelay, retryDelay(nil, 20))
	rae := &retryAfterError{err: &Error{Status: 429}, after: 7 * time.Second}
	require.Equal(t, 7*time.Second, retryDelay(rae, 1))
	require.Equal(t, maxRetryDelay, retryDelay(&retryAfterError{err: &Error{Status: 429}, after: time.Hour}, 1))
}

func TestRetryAfterHeader(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	require.Equal(t, time.Duration(0), retryAfter(resp))
	resp.Header.Set("Retry-After", "2.5")
	require.Equal(t, 2500*time.Millisecond, retryAfter(resp))
	resp.Header.Set("Retry-After", "garbage")
	require.Equal(t, time.Duration(0), retryAfter(resp))
}

func TestReadBounded(t *testing.T) {
	data, err := readBounded(bytes.NewReader([]byte("abc")), 3)
	require.NoError(t, err)
	require.Equal(t, "abc", string(data))
	_, err = readBounded(bytes.NewReader([]byte("abcd")), 3)
	require.Error(t, err)
}
