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
