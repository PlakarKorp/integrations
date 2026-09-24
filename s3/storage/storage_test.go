package storage

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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

// Concurrent Gets must reuse connections once the first batch has dialed,
// rather than dropping all but a few and redialing on the next batch.
func TestGetReusesConnections(t *testing.T) {
	const inflight = 64
	const rounds = 10

	var mu sync.Mutex
	var arrived int
	barrier := make(chan struct{})

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// hold every request until the whole batch is in flight, so they
		// overlap and each needs its own connection.
		mu.Lock()
		arrived++
		b := barrier
		if arrived == inflight {
			close(b)
		}
		mu.Unlock()
		<-b
		w.Header().Set("Last-Modified", "Mon, 07 Sep 2026 00:00:00 GMT")
		w.Header().Set("ETag", `"etag"`)
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("data"))
	}))
	var dials atomic.Int64
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			dials.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(context.Background(), "s3", map[string]string{
		"location":          "s3://" + u.Host + "/bucket",
		"access_key":        "test",
		"secret_access_key": "test",
		"use_tls":           "false",
		"region":            "us-east-1",
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	for range rounds {
		mu.Lock()
		arrived = 0
		barrier = make(chan struct{})
		mu.Unlock()

		var wg sync.WaitGroup
		for range inflight {
			wg.Go(func() {
				rd, err := st.Get(context.Background(), storage.StorageResourcePackfile, objects.MAC{}, &storage.Range{Offset: 0, Length: 4})
				if err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				defer rd.Close()
				if _, err := io.ReadAll(rd); err != nil {
					t.Errorf("read: %v", err)
				}
			})
		}
		wg.Wait()
	}

	if n := dials.Load(); n > inflight {
		t.Fatalf("%d connections dialed for %d concurrent requests over %d rounds, want at most %d", n, inflight, rounds, inflight)
	}
}
