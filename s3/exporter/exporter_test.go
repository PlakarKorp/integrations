package exporter

import (
	"context"
<<<<<<< HEAD
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewS3ExporterVirtualHostRoot(t *testing.T) {
	// NewS3Exporter creates the bucket, so it needs an endpoint that accepts it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	endpoint := u.Host

	tests := []struct {
		name     string
		host     string
		path     string
		root     string
		wantRoot string
		wantErr  bool
	}{
		{name: "no root", wantRoot: "/"},
		{name: "bare bucket host with root", host: "bucket", root: "/my/subdir", wantRoot: "/my/subdir"},
		{name: "default root", root: "/", wantRoot: "/"},
		{name: "root is the prefix", root: "/plakar/demo/", wantRoot: "/plakar/demo"},
		{name: "location path only", path: "/data", wantRoot: "/data"},
		{name: "location path and same root", path: "/data", root: "/data/", wantRoot: "/data"},
		{name: "location path and default root", path: "/data", root: "/", wantRoot: "/data"},
		{name: "location path and other root", path: "/data", root: "/other", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := tt.host
			if host == "" {
				host = "bucket." + endpoint
			}
			config := map[string]string{
				"location":          "s3://" + host + tt.path,
				"access_key":        "access",
				"secret_access_key": "secret",
				"use_tls":           "false",
				"virtual_host":      "true",
				"endpoint":          endpoint,
			}
			if tt.root != "" {
				config["root"] = tt.root
			}

			exp, err := NewS3Exporter(context.Background(), nil, "s3", config)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			require.Equal(t, tt.wantRoot, exp.Root())
			require.Equal(t, endpoint+"/bucket", exp.Origin())
		})
	}
}

func TestNewS3ExporterPathStyleRoot(t *testing.T) {
	// NewS3Exporter creates the bucket, so it needs an endpoint that accepts it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	endpoint := u.Host

	tests := []struct {
		name     string
		location string
		root     string
		wantRoot string
		wantErr  bool
	}{
		{name: "no root", location: "s3://" + endpoint + "/bucket/data", wantRoot: "/data"},
		{name: "root is the prefix", location: "s3://" + endpoint + "/bucket", root: "/plakar/demo", wantRoot: "/plakar/demo"},
		{name: "root includes the bucket", location: "s3://" + endpoint, root: "/bucket/data", wantRoot: "/data"},
		{name: "bucket in location host", location: "s3://bucket/plakar", root: "/plakar", wantRoot: "/plakar"},
		{name: "root that starts with the bucket", location: "s3://bucket/bucket/plakar", root: "/bucket/plakar", wantRoot: "/plakar"},
		{name: "location path and other root", location: "s3://" + endpoint + "/bucket/data", root: "/other", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]string{
				"location":          tt.location,
				"access_key":        "access",
				"secret_access_key": "secret",
				"use_tls":           "false",
				"virtual_host":      "false",
				"endpoint":          endpoint,
			}
			if tt.root != "" {
				config["root"] = tt.root
			}

			exp, err := NewS3Exporter(context.Background(), nil, "s3", config)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			require.Equal(t, tt.wantRoot, exp.Root())
			require.Equal(t, endpoint+"/bucket", exp.Origin())
		})
	}
}

func TestNewS3ExporterPathStyleRootIncludesBucketWithoutEndpoint(t *testing.T) {
	// NewS3Exporter creates the bucket, so it needs an endpoint that accepts it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	exp, err := NewS3Exporter(context.Background(), nil, "s3", map[string]string{
		"location":          "s3://" + u.Host,
		"access_key":        "access",
		"secret_access_key": "secret",
		"use_tls":           "false",
		"virtual_host":      "false",
		"root":              "/bucket/my/subdir",
	})
	require.NoError(t, err)

	require.Equal(t, "/my/subdir", exp.Root())
	require.Equal(t, u.Host+"/bucket", exp.Origin())
=======
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

// runExport exports one record per key through a fake registry, returning
// the set of object keys the fake server actually received a PUT for.
func runExport(t *testing.T, extraConfig map[string]string, keys []string) map[string]bool {
	t.Helper()

	puts := make(map[string]bool)
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("location") {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		if r.Method == http.MethodPut && r.URL.Path != "/bucket" {
			mu.Lock()
			puts[strings.TrimPrefix(r.URL.Path, "/bucket/")] = true
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	host := strings.TrimPrefix(srv.URL, "http://")
	config := map[string]string{
		"location":          "s3://" + host + "/bucket",
		"access_key":        "test",
		"secret_access_key": "test",
		"use_tls":           "false",
	}
	for k, v := range extraConfig {
		config[k] = v
	}

	exp, err := NewS3Exporter(context.Background(), &connectors.Options{MaxConcurrency: 1}, "s3", config)
	if err != nil {
		t.Fatal(err)
	}

	records := make(chan *connectors.Record, len(keys))
	for _, k := range keys {
		records <- connectors.NewRecord(k, "", objects.FileInfo{
			Lname: k,
			Lmode: 0o600,
		}, nil, func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("x")), nil
		})
	}
	close(records)

	results := make(chan *connectors.Result, len(keys))
	if err := exp.Export(context.Background(), records, results); err != nil {
		t.Fatal(err)
	}
	for range keys {
		res := <-results
		if res.Err != nil {
			t.Fatalf("export failed: %v", res.Err)
		}
	}

	return puts
}

// strict is the default: the escaped form from a strict-mode s3 import is
// written to the destination bucket as-is.
func TestExportKeepsEscapedPathByDefault(t *testing.T) {
	puts := runExport(t, nil, []string{"/a/%2e"})
	if !puts["a/%2e"] {
		t.Fatalf("got puts %v, want a/%%2e kept escaped", puts)
	}
}

// strict=false decodes the escaped form back to the original key, for a
// round-trip restore of data that actually came from a strict-mode import.
func TestExportDecodesEscapedPathWhenNotStrict(t *testing.T) {
	puts := runExport(t, map[string]string{"strict": "false"}, []string{"/a/%2e", "/a/%252e"})
	if !puts["a/."] {
		t.Fatalf("got puts %v, want a/. decoded", puts)
	}
	if !puts["a/%2e"] {
		t.Fatalf("got puts %v, want a/%%2e decoded from a/%%252e", puts)
	}
}

func TestNewS3ExporterRejectsInvalidStrict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	host := strings.TrimPrefix(srv.URL, "http://")
	_, err := NewS3Exporter(context.Background(), &connectors.Options{MaxConcurrency: 1}, "s3", map[string]string{
		"location":          "s3://" + host + "/bucket",
		"access_key":        "test",
		"secret_access_key": "test",
		"use_tls":           "false",
		"strict":            "maybe",
	})
	if err == nil {
		t.Fatal("invalid strict value was accepted")
	}
>>>>>>> bc3840f (s3/exporter: add strict mode to match the importer)
}
