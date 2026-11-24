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

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{Token: "tok", BaseURL: srv.URL, Rate: 1000, Concurrency: 2})
	require.NoError(t, err)
	return c, srv
}

func TestNewNeedsToken(t *testing.T) {
	_, err := New(Config{})
	require.ErrorIs(t, err, ErrMissingToken)
}

func TestHeadersAndErrors(t *testing.T) {
	var got http.Header
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if r.URL.Path == "/v1/pages/missing" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"object":"error","status":404,"code":"object_not_found","message":"nope"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"object":"user","id":"u1"}`)
	}))
	raw, err := c.Me(context.Background())
	require.NoError(t, err)
	require.JSONEq(t, `{"object":"user","id":"u1"}`, string(raw))
	require.Equal(t, "Bearer tok", got.Get("Authorization"))
	require.Equal(t, Version, got.Get("Notion-Version"))

	_, err = c.Page(context.Background(), "missing")
	require.True(t, IsNotFound(err))
	require.False(t, IsValidation(err))
	require.Contains(t, err.Error(), "object_not_found")
}

func TestRateLimitRetry(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"code":"rate_limited","message":"slow down"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"object":"user","id":"u1"}`)
	}))
	start := time.Now()
	_, err := c.Me(context.Background())
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
	require.GreaterOrEqual(t, time.Since(start), time.Second)
}

func TestServerErrorsOnlyRetryReads(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method == http.MethodPost || n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"code":"bad_gateway","message":"x"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"object":"page","id":"p1"}`)
	}))
	_, err := c.Page(context.Background(), "p1")
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())

	calls.Store(0)
	_, err = c.CreatePage(context.Background(), map[string]any{})
	require.Error(t, err)
	require.Equal(t, int32(1), calls.Load(), "a write must not be replayed after a 5xx")
}

func TestPagination(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("start_cursor")
		assert.Equal(t, "100", r.URL.Query().Get("page_size"))
		switch cursor {
		case "":
			_, _ = fmt.Fprint(w, `{"object":"list","results":[{"id":"1"},{"id":"2"}],"has_more":true,"next_cursor":"c2"}`)
		case "c2":
			_, _ = fmt.Fprint(w, `{"object":"list","results":[{"id":"3"}],"has_more":false,"next_cursor":null}`)
		default:
			t.Errorf("unexpected cursor %q", cursor)
		}
	}))
	var ids []string
	err := c.Users(context.Background(), func(raw json.RawMessage) error {
		var v struct{ ID string }
		require.NoError(t, json.Unmarshal(raw, &v))
		ids = append(ids, v.ID)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"1", "2", "3"}, ids)
}

// TestQueryResumes checks that a query cut at the server's limit goes
// on from the creation time of the last row, without repeating rows.
func TestQueryResumes(t *testing.T) {
	rows := []string{"2026-01-01T00:00:00.000Z", "2026-01-02T00:00:00.000Z", "2026-01-02T00:00:00.000Z", "2026-01-03T00:00:00.000Z"}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		after := ""
		if f, ok := body["filter"].(map[string]any); ok {
			after = f["created_time"].(map[string]any)["on_or_after"].(string)
		}
		var results []map[string]any
		for i, ct := range rows {
			if ct >= after {
				results = append(results, map[string]any{"id": strconv.Itoa(i), "created_time": ct})
			}
		}
		status := map[string]any{"type": "complete"}
		if len(results) > 3 {
			results = results[:3]
			status = map[string]any{"type": "incomplete", "incomplete_reason": "query_result_limit_reached"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "results": results, "has_more": false, "request_status": status})
	}))
	var ids []string
	err := c.QueryDataSource(context.Background(), "ds", func(raw json.RawMessage) error {
		var v struct{ ID string }
		require.NoError(t, json.Unmarshal(raw, &v))
		ids = append(ids, v.ID)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"0", "1", "2", "3"}, ids)
}

func TestDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"), "the token must not leak to the file host")
		if r.URL.Query().Get("expired") == "1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Length", "5")
		_, _ = fmt.Fprint(w, "hello")
	}))
	defer srv.Close()
	c, err := New(Config{Token: "tok", BaseURL: srv.URL})
	require.NoError(t, err)

	rc, size, err := c.Download(context.Background(), srv.URL+"/f/x.png")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	require.Equal(t, "hello", string(data))
	require.Equal(t, int64(5), size)

	_, _, err = c.Download(context.Background(), srv.URL+"/f/x.png?expired=1")
	require.ErrorIs(t, err, ErrExpiredURL)
}

func TestUpload(t *testing.T) {
	type part struct {
		number string
		data   []byte
	}
	var created []map[string]any
	parts := map[string][]part{}
	completed := map[string]bool{}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/file_uploads":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			created = append(created, body)
			_, _ = fmt.Fprintf(w, `{"object":"file_upload","id":"up%d","status":"pending"}`, len(created))
		case "/v1/file_uploads/up1/send", "/v1/file_uploads/up2/send":
			assert.NoError(t, r.ParseMultipartForm(64<<20))
			f, _, err := r.FormFile("file")
			if !assert.NoError(t, err) {
				return
			}
			data, _ := io.ReadAll(f)
			id := r.URL.Path[len("/v1/file_uploads/") : len("/v1/file_uploads/")+3]
			parts[id] = append(parts[id], part{r.FormValue("part_number"), data})
			_, _ = fmt.Fprint(w, `{"object":"file_upload","status":"uploaded"}`)
		case "/v1/file_uploads/up2/complete":
			completed["up2"] = true
			_, _ = fmt.Fprint(w, `{"object":"file_upload","status":"uploaded"}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"code":"validation_error","message":"unexpected %s %s"}`, r.Method, r.URL.Path)
		}
	}))

	id, err := c.Upload(context.Background(), "a.txt", "", 5, io.NopCloser(stringsReader("hello")))
	require.NoError(t, err)
	require.Equal(t, "up1", id)
	require.Equal(t, "single_part", created[0]["mode"])
	require.Equal(t, "text/plain; charset=utf-8", created[0]["content_type"])
	require.Len(t, parts["up1"], 1)
	require.Empty(t, parts["up1"][0].number)
	require.Equal(t, "hello", string(parts["up1"][0].data))

	big := make([]byte, 25<<20)
	for i := range big {
		big[i] = byte(i)
	}
	id, err = c.Upload(context.Background(), "big.bin", "application/octet-stream", int64(len(big)), bytesReader(big))
	require.NoError(t, err)
	require.Equal(t, "up2", id)
	require.Equal(t, "multi_part", created[1]["mode"])
	require.InDelta(t, 3.0, created[1]["number_of_parts"], 0)
	require.Len(t, parts["up2"], 3)
	require.Equal(t, []string{"1", "2", "3"}, []string{parts["up2"][0].number, parts["up2"][1].number, parts["up2"][2].number})
	require.Len(t, parts["up2"][0].data, 10<<20)
	require.Len(t, parts["up2"][2].data, 5<<20)
	require.True(t, completed["up2"])

	_, err = c.Upload(context.Background(), "short.bin", "", 10, stringsReader("abc"))
	require.Error(t, err, "a reader shorter than announced must fail")
}

func TestObjects(t *testing.T) {
	raw := json.RawMessage(`{"object":"page","id":"11111111-2222-3333-4444-555555555555","parent":{"type":"page_id","page_id":"p"},
		"last_edited_time":"2026-01-02T03:04:00.000Z","icon":{"type":"file","file":{"url":"https://prod-files-secure.s3.us-west-2.amazonaws.com/ws/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/icon.png?sig=1","expiry_time":"x"}},
		"properties":{"Name":{"id":"title","type":"title","title":[{"type":"text","plain_text":"Hello "},{"type":"text","plain_text":"World"}]},
		"Files":{"type":"files","files":[{"name":"doc.pdf","type":"file","file":{"url":"https://file.notion.so/f/f/ws/99999999-8888-7777-6666-555555555555/doc%20v2.pdf?table=block","expiry_time":"x"}},{"type":"external","external":{"url":"https://example.com/x.pdf"}}]}}}`)
	o, err := ParseObject(raw)
	require.NoError(t, err)
	require.Equal(t, "Hello World", o.TitleText())
	require.Equal(t, "Name", o.TitlePropertyName())
	require.Equal(t, "p", o.Parent.ID())
	require.Equal(t, 2026, o.LastEditedTime.Year())

	refs := FileRefs(raw)
	require.Len(t, refs, 2)
	keys := map[string]string{}
	for _, r := range refs {
		keys[r.Key] = r.Name
	}
	require.Equal(t, "icon.png", keys["aaaaaaaabbbbccccddddeeeeeeeeeeee/icon.png"])
	require.Equal(t, "doc.pdf", keys["99999999888877776666555555555555/doc v2.pdf"])

	require.Equal(t, "11111111-2222-3333-4444-555555555555", IDFromURL("https://www.notion.so/My-Page-11111111222233334444555555555555"))
	require.Equal(t, "11111111-2222-3333-4444-555555555555", IDFromURL("https://www.notion.so/11111111-2222-3333-4444-555555555555?v=1"))
	require.Empty(t, IDFromURL("https://www.notion.so/not-an-id"))
	require.Equal(t, "11111111222233334444555555555555", CompactID("11111111-2222-3333-4444-555555555555"))
	require.Equal(t, "11111111-2222-3333-4444-555555555555", ExpandID("11111111222233334444555555555555"))
	require.True(t, IsNotionFileURL("https://file.notion.so/f/f/a/b/c.png?x=1"))
	require.True(t, IsNotionFileURL("https://prod-files-secure.s3.us-west-2.amazonaws.com/a/b/c.png"))
	require.False(t, IsNotionFileURL("https://example.com/a/b/c.png"))

	_, ok := AssetKey("https://example.com/notanid/file.png")
	require.False(t, ok)
	k, ok := AssetKey("https://file.notion.so/f/f/ws/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/..?x")
	require.True(t, ok)
	require.Equal(t, "aaaaaaaabbbbccccddddeeeeeeeeeeee/file", k)
}

// TestDownloadStaysOnNotionHosts: a snapshot or an API answer naming
// another host must not make plakar fetch it, directly or through a
// redirect.
func TestDownloadStaysOnNotionHosts(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "secret")
	}))
	defer other.Close()
	notion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusFound)
	}))
	defer notion.Close()
	c, err := New(Config{Token: "tok", BaseURL: notion.URL})
	require.NoError(t, err)

	_, _, err = c.Download(context.Background(), other.URL+"/f/x.png")
	require.ErrorContains(t, err, "not a Notion file host")

	_, _, err = c.Download(context.Background(), notion.URL+"/files/x.png")
	require.ErrorContains(t, err, "refused")
}
