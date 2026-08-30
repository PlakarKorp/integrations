package storage

import (
	"context"
	"net/http"
	"strings"
	"testing"
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

	resp := &http.Response{
		Status: "500 Internal Server Error",
		Body:   readCloser(strings.Repeat("A", maxErrorBody*4)),
	}
	if got := errorBody(resp); len(got) > maxErrorBody {
		t.Errorf("error body not capped: %d bytes", len(got))
	}

	resp = &http.Response{
		Status: "400 Bad Request",
		Body:   readCloser("bad \x1b[2Jrequest\x00"),
	}
	if got := errorBody(resp); strings.ContainsAny(got, "\x1b\x00") {
		t.Errorf("control characters survived: %q", got)
	}
}

func readCloser(s string) *nopCloser { return &nopCloser{strings.NewReader(s)} }

type nopCloser struct{ *strings.Reader }

func (n *nopCloser) Close() error { return nil }
