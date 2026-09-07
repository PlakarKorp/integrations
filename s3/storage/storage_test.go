package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func newTestStore(t *testing.T, handler http.HandlerFunc) *Store {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4("test", "test", ""),
		Secure:       false,
		Region:       "us-east-1",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Store{minioClient: client, bucket: "bucket"}
}

const listPage = `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
	<Name>bucket</Name>
	<Prefix>packfiles/</Prefix>
	<IsTruncated>%TRUNCATED%</IsTruncated>
	<NextContinuationToken>%TOKEN%</NextContinuationToken>
	<Contents>
		<Key>packfiles/ab/%MAC%</Key>
		<LastModified>2026-09-07T00:00:00.000Z</LastModified>
		<ETag>&quot;etag&quot;</ETag>
		<Size>1</Size>
	</Contents>
	<Contents>
		<Key>packfiles/ab/not-a-mac</Key>
		<LastModified>2026-09-07T00:00:00.000Z</LastModified>
		<ETag>&quot;etag&quot;</ETag>
		<Size>1</Size>
	</Contents>
</ListBucketResult>`

const listError = `<?xml version="1.0" encoding="UTF-8"?>
<Error>
	<Code>AccessDenied</Code>
	<Message>Access Denied.</Message>
	<BucketName>bucket</BucketName>
	<Resource>/bucket/</Resource>
	<RequestId>req</RequestId>
	<HostId>host</HostId>
</Error>`

func page(macHex string, token string) string {
	body := strings.ReplaceAll(listPage, "%MAC%", macHex)
	if token == "" {
		body = strings.ReplaceAll(body, "%TRUNCATED%", "false")
		return strings.ReplaceAll(body, "%TOKEN%", "")
	}
	body = strings.ReplaceAll(body, "%TRUNCATED%", "true")
	return strings.ReplaceAll(body, "%TOKEN%", token)
}

func TestList(t *testing.T) {
	s := newTestStore(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(page(strings.Repeat("ab", 32), "")))
	})

	macs, err := s.List(context.Background(), storage.StorageResourcePackfile)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var want objects.MAC
	for i := range want {
		want[i] = 0xab
	}
	if len(macs) != 1 || macs[0] != want {
		t.Fatalf("got %v, want [%v]", macs, want)
	}
}

func TestListErrorMidListing(t *testing.T) {
	s := newTestStore(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("continuation-token") != "" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(listError))
			return
		}
		w.Write([]byte(page(strings.Repeat("ab", 32), "next")))
	})

	macs, err := s.List(context.Background(), storage.StorageResourcePackfile)
	if err == nil {
		t.Fatalf("List returned nil error with partial results %v", macs)
	}
	if minio.ToErrorResponse(err).Code != "AccessDenied" {
		t.Fatalf("unexpected error: %v", err)
	}
	if macs != nil {
		t.Fatalf("partial results returned alongside error: %v", macs)
	}
}
