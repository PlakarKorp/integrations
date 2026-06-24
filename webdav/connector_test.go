package webdav

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/emersion/go-webdav"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/*
var testdata embed.FS

var (
	file = func(p string) item {
		return item{
			name:    p,
			content: path.Base(p) + "\n",
		}
	}
	dir = func(p string) item { return item{name: p, dir: true} }
)

var contents = []item{
	dir("/"),
	dir("/epsilon"),
	dir("/gamma"),
	file("/alpha"),
	file("/beta"),
	file("/epsilon/zeta"),
	file("/gamma/delta"),
}

// testClient implements webdav.HTTPClient, which allow us to execute
// HTTP calls without actually listening on a port.
type testClient struct {
	handler http.Handler
}

func (t *testClient) Do(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	w.Body = bytes.NewBuffer(nil)

	t.handler.ServeHTTP(w, req)
	return w.Result(), nil
}

func newwebdav(ctx context.Context, client webdav.HTTPClient, proto, insecure string) (*WebDAV, error) {
	params := map[string]string{
		"location": proto + "://localhost",
	}
	if insecure != "" {
		params["insecure"] = insecure
	}

	opts := &connectors.Options{
		Hostname:        "localhost",
		OperatingSystem: runtime.GOOS,
		Architecture:    runtime.GOARCH,
		CWD:             "/nonexistent",
		MaxConcurrency:  1,
	}

	return New(ctx, opts, proto, params, client)
}

func TestMetadata(t *testing.T) {
	webdav, err := newwebdav(t.Context(), nil, "dav", "true")
	require.NoError(t, err)
	require.NotNil(t, webdav)

	require.Equal(t, "webdav", webdav.Type())
	require.Equal(t, "localhost", webdav.Origin())
	require.Equal(t, "/", webdav.Root())
	require.Equal(t, location.Flags(0), webdav.Flags())
}

func TestInsecureOption(t *testing.T) {
	suite := []struct {
		proto    string
		insecure bool
		ok       bool
	}{
		{
			proto:    "dav",
			insecure: false,
			ok:       false,
		},
		{
			proto:    "dav",
			insecure: true,
			ok:       true,
		},
		{
			proto:    "davs",
			insecure: true,
			ok:       false,
		},
		{
			proto:    "davs",
			insecure: false,
			ok:       true,
		},
	}

	for _, test := range suite {
		webdav, err := newwebdav(t.Context(), nil, test.proto, fmt.Sprint(test.insecure))

		msg := fmt.Sprintf("proto=%s insecure=%v", test.proto, test.insecure)
		if test.ok {
			require.NoError(t, err, msg)
			require.NotNil(t, webdav, msg)
		} else {
			require.Error(t, err, msg)
			require.Nil(t, webdav, msg)
		}
	}
}

func TestPing(t *testing.T) {
	handler := &webdav.Handler{
		FileSystem: &testFS{backing: testdata},
	}

	client := &testClient{handler: handler}

	webdav, err := newwebdav(t.Context(), client, "dav", "true")
	require.NoError(t, err)
	require.NotNil(t, webdav)

	err = webdav.Ping(t.Context())
	require.NotNil(t, err)
}

func TestImport(t *testing.T) {
	handler := &webdav.Handler{
		FileSystem: &testFS{backing: testdata, prefix: "testdata"},
	}

	client := &testClient{handler: handler}

	webdav, err := newwebdav(t.Context(), client, "dav", "true")
	require.NoError(t, err)
	require.NotNil(t, webdav)

	var (
		records = make(chan *connectors.Record)
		ok      = make(chan struct{})
		seen    = make(map[string]*connectors.Record)
	)

	go func() {
		for record := range records {
			if record.Err != nil {
				t.Errorf("failed for %s: %v",
					record.Pathname, record.Err)
				continue
			}

			seen[record.Pathname] = record
		}
		close(ok)
	}()

	err = webdav.Import(t.Context(), records, nil)
	<-ok
	require.NoError(t, err)

	if t.Failed() {
		t.FailNow()
	}

	for _, item := range contents {
		record, ok := seen[item.name]
		if !ok {
			t.Errorf("file %q not returned by the importer",
				item.name)
			continue
		}

		delete(seen, item.name)

		if item.dir != record.FileInfo.IsDir() {
			typ := "file"
			if item.dir {
				typ = "dir"
			}

			t.Errorf("%s %q not of the type expected", typ, item.name)

			record.Close()
			continue
		}

		if item.dir {
			record.Close()
			continue
		}

		buf, err := io.ReadAll(record.Reader)
		require.NoError(t, err)
		require.NotNil(t, buf)

		record.Close()

		require.Equal(t, item.content, string(buf))
	}

	require.Empty(t, seen, "importer yield unexpected items")
}

func TestImportFailingServer(t *testing.T) {
	var mux http.ServeMux

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "woooops", http.StatusInternalServerError)
	})

	client := &testClient{handler: &mux}

	webdav, err := newwebdav(t.Context(), client, "dav", "true")
	require.NoError(t, err)
	require.NotNil(t, webdav)

	// on purpose, because it shall fail immediately
	records := make(chan *connectors.Record)
	err = webdav.Import(t.Context(), records, nil)
	require.Error(t, err, "importer shall fail")
}

func TestExport(t *testing.T) {
	var (
		testfs  = &testFS{overlay: make(map[string]item)}
		handler = &webdav.Handler{FileSystem: testfs}
		client  = &testClient{handler: handler}
	)

	webdav, err := newwebdav(t.Context(), client, "dav", "true")
	require.NoError(t, err)
	require.NotNil(t, webdav)

	var (
		records = make(chan *connectors.Record)
		results = make(chan *connectors.Result)
		ok      = make(chan struct{})
		seen    = make(map[string]struct{})
	)

	go func() {
		defer close(records)
		for _, item := range contents {
			var (
				rd   io.ReadCloser
				size int64
				mode fs.FileMode
			)

			if !item.dir {
				rd = io.NopCloser(strings.NewReader(item.content))
				size = int64(len(item.content))
			} else {
				mode = fs.ModeDir
			}

			records <- &connectors.Record{
				Reader:   rd,
				Pathname: item.name,
				FileInfo: objects.FileInfo{
					Lname:    path.Base(item.name),
					Lsize:    size,
					Lmode:    mode,
					LmodTime: time.Now(),
				},
			}
		}
	}()

	go func() {
		for result := range results {
			pathname := result.Record.Pathname
			if result.Err != nil {
				t.Errorf("failed for %s: %v", pathname, result.Err)
				continue
			}

			seen[pathname] = struct{}{}
		}
		close(ok)
	}()

	err = webdav.Export(t.Context(), records, results)
	<-ok
	require.NoError(t, err)

	if t.Failed() {
		t.FailNow()
	}

	for _, item := range contents {
		if _, ok := seen[item.name]; !ok {
			t.Errorf("file %q not ack'ed by the exporter",
				item.name)
			continue
		}
		delete(seen, item.name)

		if _, ok := testfs.overlay[item.name]; !ok {
			t.Errorf("item %q not created on the webdav remote", item.name)
			continue
		}
		delete(testfs.overlay, item.name)
	}

	require.Empty(t, seen, "exporter ack'ed unexpected items")
	require.Empty(t, testfs.overlay, "exporter created unexpected items")
}
