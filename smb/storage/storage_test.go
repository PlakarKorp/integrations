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
	"encoding/hex"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/require"
)

type fakeEntry struct {
	name string
	dir  bool
}

func (f fakeEntry) Name() string { return f.name }
func (f fakeEntry) Size() int64  { return 0 }
func (f fakeEntry) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir
	}
	return 0
}
func (f fakeEntry) ModTime() time.Time { return time.Time{} }
func (f fakeEntry) IsDir() bool        { return f.dir }
func (f fakeEntry) Sys() any           { return nil }

func TestMacsFromEntries(t *testing.T) {
	var mac objects.MAC
	mac[0] = 0xab
	mac[31] = 0xcd
	name := hex.EncodeToString(mac[:])

	got := macsFromEntries([]os.FileInfo{
		fakeEntry{name: name},
		fakeEntry{name: name, dir: true},          // a directory, even if well named
		fakeEntry{name: ".tmp-0123456789abcdef"},  // leftover temporary file
		fakeEntry{name: "deadbeef"},               // too short
		fakeEntry{name: strings.Repeat("zz", 32)}, // not hex
	})
	require.Equal(t, []objects.MAC{mac}, got)
}

func TestTempName(t *testing.T) {
	a, err := tempName("/repo/packfiles/ab")
	require.NoError(t, err)
	b, err := tempName("/repo/packfiles/ab")
	require.NoError(t, err)

	require.Equal(t, "/repo/packfiles/ab", path.Dir(a))
	require.True(t, strings.HasPrefix(path.Base(a), ".tmp-"))
	require.NotEqual(t, a, b)
}

func TestObjectPath(t *testing.T) {
	var mac objects.MAC
	mac[0] = 0x0f
	b := NewBuckets(nil, "/repo/packfiles")
	require.Equal(t, "/repo/packfiles/0f/"+hex.EncodeToString(mac[:]), b.objectPath(mac))
}
