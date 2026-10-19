package exporter

import (
	"context"
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
}
