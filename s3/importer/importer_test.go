package importer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"github.com/PlakarKorp/kloset/connectors"
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
)

func listResponse(keys []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
	<Name>bucket</Name>
	<IsTruncated>false</IsTruncated>`)
	for _, k := range keys {
		fmt.Fprintf(&b, `
	<Contents>
		<Key>%s</Key>
		<LastModified>2026-09-07T00:00:00.000Z</LastModified>
		<ETag>&quot;etag&quot;</ETag>
		<Size>1</Size>
	</Contents>`, k)
	}
	b.WriteString("\n</ListBucketResult>")
	return b.String()
}

var dotKeys = []string{".", "..", "a/..", "a/.", "a/b/..", "a/file", "..foo", "a/.hidden"}

func importKeys(t *testing.T, keys []string, extraConfig map[string]string) ([]string, error) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Has("location") {
			_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		_, _ = fmt.Fprint(w, listResponse(keys))
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

	imp, err := NewS3Importer(context.Background(), nil, "s3", config)
	if err != nil {
		return nil, err
	}

	records := make(chan *connectors.Record)
	errch := make(chan error, 1)
	go func() { errch <- imp.Import(context.Background(), records, nil) }()

	var got []string
	for rec := range records {
		got = append(got, rec.Pathname)
	}
	return got, <-errch
}

// strict mode is the default: a "." or ".." key is kept, with its basename
// escaped so it can't be read back as a directory reference.
func TestImportEscapesDotKeysByDefault(t *testing.T) {
	got, err := importKeys(t, dotKeys, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"/%2e", "/%2e%2e", "/a/%2e%2e", "/a/%2e", "/a/b/%2e%2e",
		"/a/file", "/..foo", "/a/.hidden",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got records %q, want %q", got, want)
	}
}

// strict=false preserves the pre-fix behavior of dropping those keys, for
// compatibility with buckets that were already scanned under it.
func TestImportSkipsDotKeysWhenNotStrict(t *testing.T) {
	got, err := importKeys(t, dotKeys, map[string]string{"strict": "false"})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"/a/file", "/..foo", "/a/.hidden"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got records %q, want %q", got, want)
	}
}

func TestNewS3ImporterRejectsInvalidStrict(t *testing.T) {
	_, err := importKeys(t, nil, map[string]string{"strict": "maybe"})
	if err == nil {
		t.Fatal("invalid strict value was accepted")
	}
}

// A "." or ".." segment in the middle of a key, not just its basename,
// would otherwise be collapsed by kloset's path.Clean-based tree building
// and relocate the record elsewhere in the snapshot.
func TestImportEscapesEmbeddedDotSegments(t *testing.T) {
	got, err := importKeys(t, []string{"data/../../../etc/passwd", "a/../b/c"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"/data/%2e%2e/%2e%2e/%2e%2e/etc/passwd",
		"/a/%2e%2e/b/c",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got records %q, want %q", got, want)
	}
}

func TestImportSkipsEmbeddedDotSegmentsWhenNotStrict(t *testing.T) {
	got, err := importKeys(t, []string{"data/../../../etc/passwd", "a/../b/c", "a/b/c"}, map[string]string{"strict": "false"})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"/a/b/c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got records %q, want %q", got, want)
	}
}

// A key that already contains a literal "%2e" must not collide with the
// escaped form of a "." segment once both are percent-encoded.
func TestImportEscapesPercentCollision(t *testing.T) {
	got, err := importKeys(t, []string{"a/.", "a/%2e"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"/a/%2e", "/a/%252e"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got records %q, want %q", got, want)
	}
}
