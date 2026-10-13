package common

import "testing"

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
