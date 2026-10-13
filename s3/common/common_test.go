package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitPathStyle(t *testing.T) {
	tests := []struct {
		name         string
		locationPath string
		root         string
		wantBucket   string
		wantPrefix   string
		wantErr      bool
	}{
		{name: "bucket only", locationPath: "/bucket", wantBucket: "bucket"},
		{name: "bucket and prefix", locationPath: "/bucket/data/plakar", wantBucket: "bucket", wantPrefix: "data/plakar"},
		{name: "default root keeps the location", locationPath: "/bucket/data", root: "/", wantBucket: "bucket", wantPrefix: "data"},
		{name: "root is the prefix", locationPath: "/bucket", root: "/data/plakar", wantBucket: "bucket", wantPrefix: "data/plakar"},
		{name: "location and root carry the same prefix", locationPath: "/bucket/data", root: "/data/", wantBucket: "bucket", wantPrefix: "data/"},
		{name: "root that starts with the bucket", locationPath: "/bucket", root: "/bucket/data", wantBucket: "bucket", wantPrefix: "data"},
		{name: "root that repeats the bucket after it", locationPath: "/bucket/bucket/data", root: "/bucket/data", wantBucket: "bucket", wantPrefix: "data"},
		{name: "no bucket in the location", locationPath: "", root: "/bucket/data", wantBucket: "bucket", wantPrefix: "data"},
		{name: "location prefix and other root", locationPath: "/bucket/data", root: "/other", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, prefix, err := SplitPathStyle(tt.locationPath, tt.root)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got bucket %q prefix %q", bucket, prefix)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitPathStyle: %v", err)
			}
			if bucket != tt.wantBucket || prefix != tt.wantPrefix {
				t.Errorf("bucket, prefix = %q, %q, want %q, %q", bucket, prefix, tt.wantBucket, tt.wantPrefix)
			}
		})
	}
}

func TestSplitVirtualHost(t *testing.T) {
	tests := []struct {
		name       string
		hostname   string
		endpoint   string
		wantBucket string
		wantHost   string
		wantErr    bool
	}{
		{name: "bucket under the endpoint", hostname: "mybucket.s3.eu-west-3.amazonaws.com", endpoint: "s3.eu-west-3.amazonaws.com", wantBucket: "mybucket", wantHost: "s3.eu-west-3.amazonaws.com"},
		{name: "bare bucket", hostname: "mybucket", endpoint: "s3.eu-west-3.amazonaws.com", wantBucket: "mybucket", wantHost: "s3.eu-west-3.amazonaws.com"},
		{name: "endpoint with scheme and trailing slash", hostname: "mybucket.s3.eu-west-3.amazonaws.com", endpoint: "https://s3.eu-west-3.amazonaws.com/", wantBucket: "mybucket", wantHost: "s3.eu-west-3.amazonaws.com"},
		{name: "fully qualified hostname", hostname: "mybucket.s3.eu-west-3.amazonaws.com.", endpoint: "s3.eu-west-3.amazonaws.com", wantBucket: "mybucket", wantHost: "s3.eu-west-3.amazonaws.com"},
		{name: "empty endpoint", hostname: "mybucket.s3.eu-west-3.amazonaws.com", endpoint: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, host, err := SplitVirtualHost(tt.hostname, tt.endpoint)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantBucket, bucket)
			require.Equal(t, tt.wantHost, host)
		})
	}
}

func TestVirtualHostPrefix(t *testing.T) {
	tests := []struct {
		name         string
		locationPath string
		root         string
		wantPrefix   string
		wantErr      bool
	}{
		{name: "neither", wantPrefix: ""},
		{name: "default root", root: "/", wantPrefix: ""},
		{name: "location path only", locationPath: "/data", wantPrefix: "data"},
		{name: "root only", root: "/my/subdir/", wantPrefix: "my/subdir"},
		{name: "location path and default root", locationPath: "/data", root: "/", wantPrefix: "data"},
		{name: "location path and same root", locationPath: "/data", root: "/data/", wantPrefix: "data"},
		{name: "location path and other root", locationPath: "/data", root: "/other", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix, err := VirtualHostPrefix(tt.locationPath, tt.root)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantPrefix, prefix)
		})
	}
}
