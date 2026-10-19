package importer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewS3ImporterVirtualHostRoot(t *testing.T) {
	tests := []struct {
		name     string
		location string
		root     string
		wantRoot string
		wantErr  bool
	}{
		{name: "no root", location: "s3://bucket.s3.eu-west-3.amazonaws.com", wantRoot: "/"},
		{name: "default root", location: "s3://bucket.s3.eu-west-3.amazonaws.com", root: "/", wantRoot: "/"},
		{name: "root is the prefix", location: "s3://bucket.s3.eu-west-3.amazonaws.com", root: "/plakar/demo/", wantRoot: "/plakar/demo"},
		{name: "bare bucket host with root", location: "s3://bucket", root: "/my/subdir", wantRoot: "/my/subdir"},
		{name: "location path only", location: "s3://bucket.s3.eu-west-3.amazonaws.com/data", wantRoot: "/data"},
		{name: "location path and same root", location: "s3://bucket.s3.eu-west-3.amazonaws.com/data", root: "/data/", wantRoot: "/data"},
		{name: "location path and default root", location: "s3://bucket.s3.eu-west-3.amazonaws.com/data", root: "/", wantRoot: "/data"},
		{name: "location path and other root", location: "s3://bucket.s3.eu-west-3.amazonaws.com/data", root: "/other", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]string{
				"location":          tt.location,
				"access_key":        "access",
				"secret_access_key": "secret",
				"virtual_host":      "true",
				"endpoint":          "s3.eu-west-3.amazonaws.com",
			}
			if tt.root != "" {
				config["root"] = tt.root
			}

			imp, err := NewS3Importer(context.Background(), nil, "s3", config)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			require.Equal(t, tt.wantRoot, imp.Root())
			require.Equal(t, "s3.eu-west-3.amazonaws.com/bucket", imp.Origin())
		})
	}
}

func TestNewS3ImporterPathStyleRoot(t *testing.T) {
	const endpoint = "s3.eu-west-3.amazonaws.com"

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
				"virtual_host":      "false",
				"endpoint":          endpoint,
			}
			if tt.root != "" {
				config["root"] = tt.root
			}

			imp, err := NewS3Importer(context.Background(), nil, "s3", config)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			require.Equal(t, tt.wantRoot, imp.Root())
			require.Equal(t, endpoint+"/bucket", imp.Origin())
		})
	}
}

func TestNewS3ImporterPathStyleRootIncludesBucketWithoutEndpoint(t *testing.T) {
	imp, err := NewS3Importer(context.Background(), nil, "s3", map[string]string{
		"location":          "s3://s3.eu-west-3.amazonaws.com",
		"access_key":        "access",
		"secret_access_key": "secret",
		"virtual_host":      "false",
		"root":              "/bucket/my/subdir",
	})
	require.NoError(t, err)

	require.Equal(t, "/my/subdir", imp.Root())
	require.Equal(t, "s3.eu-west-3.amazonaws.com/bucket", imp.Origin())
}
