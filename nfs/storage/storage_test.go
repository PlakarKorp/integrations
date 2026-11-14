/*
 * Copyright (c) 2025 Gilles Chehade <gilles@poolp.org>
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
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/PlakarKorp/kloset/objects"
)

func TestTempName(t *testing.T) {
	a, err := tempName("/packfiles")
	if err != nil {
		t.Fatalf("tempName: %v", err)
	}
	b, err := tempName("/packfiles")
	if err != nil {
		t.Fatalf("tempName: %v", err)
	}
	if a == b {
		t.Fatalf("tempName returned the same name twice: %q", a)
	}
	if got, want := a[:len("/packfiles/.tmp-")], "/packfiles/.tmp-"; got != want {
		t.Errorf("tempName = %q, want prefix %q", a, want)
	}
}

func TestCountingReader(t *testing.T) {
	data := []byte("hello, nfs storage")
	cr := &countingReader{r: bytes.NewReader(data)}

	got, err := io.ReadAll(cr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("read %q, want %q", got, data)
	}
	if cr.n != int64(len(data)) {
		t.Errorf("countingReader.n = %d, want %d", cr.n, len(data))
	}
}

func TestBucketPath(t *testing.T) {
	b := NewBuckets(nil, "/packfiles")

	var mac objects.MAC
	copy(mac[:], bytes.Repeat([]byte{0xab}, len(mac)))

	got := b.bucketPath(mac)
	want := "/packfiles/ab/" + strings.Repeat("ab", len(mac))
	if got != want {
		t.Errorf("bucketPath = %q, want %q", got, want)
	}
}
