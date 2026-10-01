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

package storage

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The auth token is a bearer credential; over http:// it is readable on path.
func TestNewStoreRefusesTokenOverCleartext(t *testing.T) {
	_, err := NewStore(context.Background(), "http", map[string]string{
		"location":   "http://repo.example/backups",
		"auth_token": "s3cret",
	})
	if err == nil {
		t.Fatal("auth_token over http:// was accepted")
	}

	if _, err := NewStore(context.Background(), "http", map[string]string{
		"location":   "http://repo.example/backups",
		"auth_token": "s3cret",
		"insecure":   "true",
	}); err != nil {
		t.Errorf("explicit opt-in rejected: %v", err)
	}

	if _, err := NewStore(context.Background(), "https", map[string]string{
		"location":   "https://repo.example/backups",
		"auth_token": "s3cret",
	}); err != nil {
		t.Errorf("https rejected: %v", err)
	}

	// No token, no credential to leak.
	if _, err := NewStore(context.Background(), "http", map[string]string{
		"location": "http://repo.example/backups",
	}); err != nil {
		t.Errorf("tokenless http rejected: %v", err)
	}
}

// tls_no_verify disables certificate verification, which is just as
// insecure for a bearer credential as plain http://.
func TestNewStoreRefusesTokenOverUnverifiedTLS(t *testing.T) {
	_, err := NewStore(context.Background(), "https", map[string]string{
		"location":      "https://repo.example/backups",
		"auth_token":    "s3cret",
		"tls_no_verify": "true",
	})
	require.Error(t, err, "auth_token over tls_no_verify was accepted")

	_, err = NewStore(context.Background(), "https", map[string]string{
		"location":      "https://repo.example/backups",
		"auth_token":    "s3cret",
		"tls_no_verify": "true",
		"insecure":      "true",
	})
	assert.NoError(t, err, "explicit opt-in rejected")
}

// URL userinfo is sent as Basic Auth by net/http on every request, so it is
// a credential just like auth_token.
func TestNewStoreRefusesURLUserinfoOverInsecureChannel(t *testing.T) {
	_, err := NewStore(context.Background(), "http", map[string]string{
		"location": "http://user:pass@repo.example/backups",
	})
	require.Error(t, err, "URL userinfo over http:// was accepted")

	_, err = NewStore(context.Background(), "http", map[string]string{
		"location": "http://user:pass@repo.example/backups",
		"insecure": "true",
	})
	assert.NoError(t, err, "explicit opt-in rejected")

	_, err = NewStore(context.Background(), "https", map[string]string{
		"location":      "https://user:pass@repo.example/backups",
		"tls_no_verify": "true",
	})
	require.Error(t, err, "URL userinfo over tls_no_verify was accepted")

	_, err = NewStore(context.Background(), "https", map[string]string{
		"location":      "https://user:pass@repo.example/backups",
		"tls_no_verify": "true",
		"insecure":      "true",
	})
	assert.NoError(t, err, "explicit opt-in rejected")
}

func mustRequest(t *testing.T, rawurl string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("GET", rawurl, nil)
	require.NoError(t, err)
	return req
}

func TestCheckRedirect(t *testing.T) {
	origin := mustRequest(t, "https://repo.example/backups")

	err := checkRedirect(mustRequest(t, "http://repo.example/backups"), []*http.Request{origin})
	assert.Error(t, err, "scheme downgrade was accepted")

	err = checkRedirect(mustRequest(t, "https://evil.example/backups"), []*http.Request{origin})
	assert.Error(t, err, "redirect to a different host was accepted")

	err = checkRedirect(mustRequest(t, "https://repo.example/elsewhere"), []*http.Request{origin})
	assert.NoError(t, err, "same scheme and host rejected")
}

func TestNewStoreSetsATimeout(t *testing.T) {
	s, err := NewStore(context.Background(), "https", map[string]string{
		"location": "https://repo.example/backups",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.(*Store).httpClient.Timeout; got != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", got, defaultTimeout)
	}

	if _, err := NewStore(context.Background(), "https", map[string]string{
		"location": "https://repo.example/backups",
		"timeout":  "nonsense",
	}); err == nil {
		t.Error("invalid timeout accepted")
	}
}

// A hostile server should not be able to drive memory use or inject escape
// sequences through an error body.
func TestErrorBody(t *testing.T) {
	huge := &http.Response{
		Status: "500 Internal Server Error",
		Body:   http.NoBody,
	}
	if got := errorBody(huge); got != huge.Status {
		t.Errorf("empty body: got %q, want the status", got)
	}

	body := func(s string) io.ReadCloser {
		return io.NopCloser(strings.NewReader(s))
	}

	resp := &http.Response{
		Status: "500 Internal Server Error",
		Body:   body(strings.Repeat("A", maxErrorBody*4)),
	}
	if got := errorBody(resp); len(got) > maxErrorBody {
		t.Errorf("error body not capped: %d bytes", len(got))
	}

	resp = &http.Response{
		Status: "400 Bad Request",
		Body:   body("bad \x1b[2Jrequest\x00"),
	}
	if got := errorBody(resp); strings.ContainsAny(got, "\x1b\x00") {
		t.Errorf("control characters survived: %q", got)
	}
}
