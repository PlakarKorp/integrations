/*
 * Copyright (c) 2026 Omar Polo <op@omarpolo.com>
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

package rclone

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
	rclonefs "github.com/rclone/rclone/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/rclone/rclone/backend/memory" // register memory backend
)

func newRclone(t *testing.T) *Rclone {
	t.Helper()
	ctx := context.Background()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	params := map[string]string{
		"location":    "rclone://" + name,
		"rclone_type": "memory",
	}
	opts := &connectors.Options{
		Hostname:        "localhost",
		OperatingSystem: "test",
		Architecture:    "amd64",
		MaxConcurrency:  1,
	}
	r, err := New(ctx, opts, "rclone", params)
	require.NoError(t, err)
	return r
}

func putFile(t *testing.T, r *Rclone, relpath, content string) {
	t.Helper()
	ctx := context.Background()
	body := []byte(content)
	_, err := r.fs.Put(ctx, bytes.NewReader(body), &objectinfo{&connectors.Record{
		Pathname: relpath,
		FileInfo: objects.FileInfo{
			Lname:    path.Base(relpath),
			Lsize:    int64(len(body)),
			Lmode:    0600,
			LmodTime: time.Now(),
		},
	}})
	require.NoError(t, err)
}

func TestMetadata(t *testing.T) {
	r := newRclone(t)
	assert.Equal(t, "memory", r.Type())
	assert.Equal(t, "localhost", r.Origin())
	assert.NotEmpty(t, r.Root())
}

func TestPing(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()
	// seed a file so the memory backend root exists
	putFile(t, r, "ping", "")
	require.NoError(t, r.Ping(ctx))
}

func TestImport(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()

	putFile(t, r, "alpha.txt", "hello alpha")
	putFile(t, r, "sub/beta.txt", "hello beta")

	records := make(chan *connectors.Record, 16)
	results := make(chan *connectors.Result)

	var importErr error
	go func() {
		importErr = r.Import(ctx, records, results)
	}()

	var paths []string
	for rec := range records {
		if rec.Err == nil {
			paths = append(paths, rec.Pathname)
		}
	}
	require.NoError(t, importErr)

	base := r.Root()
	sort.Strings(paths)
	assert.Contains(t, paths, path.Join(base, "alpha.txt"))
	assert.Contains(t, paths, path.Join(base, "sub/beta.txt"))
}

func TestImportFileContent(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()

	putFile(t, r, "hello.txt", "world")

	records := make(chan *connectors.Record, 16)
	results := make(chan *connectors.Result)

	var importErr error
	go func() {
		importErr = r.Import(ctx, records, results)
	}()

	base := r.Root()
	target := path.Join(base, "hello.txt")
	for rec := range records {
		if rec.Pathname == target {
			require.NotNil(t, rec.Reader)
			defer rec.Reader.Close()
			got, err := io.ReadAll(rec.Reader)
			require.NoError(t, err)
			assert.Equal(t, "world", string(got))
			return
		}
	}
	require.NoError(t, importErr)
	t.Fatalf("record %s not found", target)
}

func TestExport(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()

	records := make(chan *connectors.Record, 8)
	results := make(chan *connectors.Result, 8)

	records <- &connectors.Record{
		Pathname: "outdir",
		FileInfo: objects.FileInfo{
			Lname: "outdir",
			Lmode: 0700 | fs.ModeDir,
		},
	}
	records <- &connectors.Record{
		Pathname: "outdir/file.txt",
		FileInfo: objects.FileInfo{
			Lname: "file.txt",
			Lsize: 5,
			Lmode: 0600,
		},
		Reader: io.NopCloser(strings.NewReader("hello")),
	}
	close(records)

	err := r.Export(ctx, records, results)
	require.NoError(t, err)

	for res := range results {
		require.NoError(t, res.Err)
	}

	// verify the file landed in rclone FS
	obj, err := r.fs.NewObject(ctx, "outdir/file.txt")
	require.NoError(t, err)
	rd, err := obj.Open(ctx)
	require.NoError(t, err)
	defer rd.Close()
	got, err := io.ReadAll(rd)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestExportSkipsXattr(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()

	records := make(chan *connectors.Record, 4)
	results := make(chan *connectors.Result, 4)

	records <- &connectors.Record{
		Pathname: "file.txt",
		IsXattr:  true,
		FileInfo: objects.FileInfo{Lname: "file.txt", Lmode: 0600},
	}
	close(records)

	err := r.Export(ctx, records, results)
	require.NoError(t, err)

	for res := range results {
		require.NoError(t, res.Err)
	}

	_, err = r.fs.NewObject(ctx, "file.txt")
	assert.ErrorIs(t, err, rclonefs.ErrorObjectNotFound)
}

func TestExportSkipsRoot(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()

	records := make(chan *connectors.Record, 4)
	results := make(chan *connectors.Result, 4)

	records <- &connectors.Record{
		Pathname: "/",
		FileInfo: objects.FileInfo{Lname: "/", Lmode: 0700 | fs.ModeDir},
	}
	close(records)

	err := r.Export(ctx, records, results)
	require.NoError(t, err)

	for res := range results {
		require.NoError(t, res.Err)
	}
}

func makeMAC(b byte) objects.MAC {
	var m objects.MAC
	for i := range m {
		m[i] = b
	}
	return m
}

var testMAC = makeMAC(0xab)

func TestStorageCreateOpen(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()

	conf := []byte(`{"version":1}`)
	require.NoError(t, r.Create(ctx, conf))

	got, err := r.Open(ctx)
	require.NoError(t, err)
	assert.Equal(t, conf, got)
}

func TestStorageCreateAlreadyExists(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()

	require.NoError(t, r.Create(ctx, []byte("config")))
	err := r.Create(ctx, []byte("config"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already initialized")
}

func TestStoragePutGetRoundtrip(t *testing.T) {
	resources := []storage.StorageResource{
		storage.StorageResourcePackfile,
		storage.StorageResourceState,
		storage.StorageResourceLock,
	}

	for _, res := range resources {
		res := res
		t.Run(res.String(), func(t *testing.T) {
			r := newRclone(t)
			ctx := context.Background()
			require.NoError(t, r.Create(ctx, []byte("cfg")))

			data := []byte("payload data")
			n, err := r.Put(ctx, res, testMAC, bytes.NewReader(data))
			require.NoError(t, err)
			assert.Equal(t, int64(len(data)), n)

			rd, err := r.Get(ctx, res, testMAC, nil)
			require.NoError(t, err)
			defer rd.Close()
			got, err := io.ReadAll(rd)
			require.NoError(t, err)
			assert.Equal(t, data, got)
		})
	}
}

func TestStoragePutGetRange(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, []byte("cfg")))

	data := []byte("0123456789")
	_, err := r.Put(ctx, storage.StorageResourcePackfile, testMAC, bytes.NewReader(data))
	require.NoError(t, err)

	rg := &storage.Range{Offset: 2, Length: 5}
	rd, err := r.Get(ctx, storage.StorageResourcePackfile, testMAC, rg)
	require.NoError(t, err)
	defer rd.Close()
	got, err := io.ReadAll(rd)
	require.NoError(t, err)
	assert.Equal(t, []byte("23456"), got)
}

func TestStorageList(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, []byte("cfg")))

	mac1 := makeMAC(0x01)
	mac2 := makeMAC(0x02)

	_, err := r.Put(ctx, storage.StorageResourceState, mac1, strings.NewReader("a"))
	require.NoError(t, err)
	_, err = r.Put(ctx, storage.StorageResourceState, mac2, strings.NewReader("b"))
	require.NoError(t, err)

	macs, err := r.List(ctx, storage.StorageResourceState)
	require.NoError(t, err)
	assert.Len(t, macs, 2)
	assert.Contains(t, macs, mac1)
	assert.Contains(t, macs, mac2)
}

func TestStorageDelete(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, []byte("cfg")))

	_, err := r.Put(ctx, storage.StorageResourcePackfile, testMAC, strings.NewReader("data"))
	require.NoError(t, err)

	require.NoError(t, r.Delete(ctx, storage.StorageResourcePackfile, testMAC))

	macs, err := r.List(ctx, storage.StorageResourcePackfile)
	require.NoError(t, err)
	assert.Empty(t, macs)
}

func TestStorageUnsupportedResource(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()

	const badRes storage.StorageResource = 99

	_, err := r.List(ctx, badRes)
	require.Error(t, err)

	_, err = r.Put(ctx, badRes, testMAC, strings.NewReader("x"))
	require.Error(t, err)

	_, err = r.Get(ctx, badRes, testMAC, nil)
	require.Error(t, err)

	err = r.Delete(ctx, badRes, testMAC)
	require.Error(t, err)
}

func TestStorageMode(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()
	mode, err := r.Mode(ctx)
	require.NoError(t, err)
	assert.Equal(t, storage.ModeRead|storage.ModeWrite, mode)
}

func TestStorageSize(t *testing.T) {
	r := newRclone(t)
	ctx := context.Background()
	size, err := r.Size(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), size)
}
