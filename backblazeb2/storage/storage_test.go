package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"

	connectorstorage "github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

type mockB2NativeClient struct {
	bucketExistsFn func(ctx context.Context, bucketName string) (bool, error)
	makeBucketFn   func(ctx context.Context, bucketName, bucketType string) (string, error)
	statObjectFn   func(ctx context.Context, bucketName, fileName string) (*B2FileInfo, error)
	putObjectFn    func(ctx context.Context, bucketName, fileName string, rd io.Reader, contentType string) (int64, error)
	getObjectFn    func(ctx context.Context, bucketName, fileName string) (io.ReadCloser, error)
	listObjectsFn  func(ctx context.Context, bucketName, prefix string) ([]B2FileInfo, error)
	removeObjectFn func(ctx context.Context, bucketName, fileName string) error
}

func (m *mockB2NativeClient) BucketExists(ctx context.Context, bucketName string) (bool, error) {
	if m.bucketExistsFn != nil {
		return m.bucketExistsFn(ctx, bucketName)
	}
	return false, nil
}

func (m *mockB2NativeClient) MakeBucket(ctx context.Context, bucketName, bucketType string) (string, error) {
	if m.makeBucketFn != nil {
		return m.makeBucketFn(ctx, bucketName, bucketType)
	}
	return "", nil
}

func (m *mockB2NativeClient) StatObject(ctx context.Context, bucketName, fileName string) (*B2FileInfo, error) {
	if m.statObjectFn != nil {
		return m.statObjectFn(ctx, bucketName, fileName)
	}
	return nil, nil
}

func (m *mockB2NativeClient) PutObject(ctx context.Context, bucketName, fileName string, rd io.Reader, contentType string) (int64, error) {
	if m.putObjectFn != nil {
		return m.putObjectFn(ctx, bucketName, fileName, rd, contentType)
	}
	return 0, nil
}

func (m *mockB2NativeClient) GetObject(ctx context.Context, bucketName, fileName string) (io.ReadCloser, error) {
	if m.getObjectFn != nil {
		return m.getObjectFn(ctx, bucketName, fileName)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (m *mockB2NativeClient) ListObjects(ctx context.Context, bucketName, prefix string) ([]B2FileInfo, error) {
	if m.listObjectsFn != nil {
		return m.listObjectsFn(ctx, bucketName, prefix)
	}
	return nil, nil
}

func (m *mockB2NativeClient) RemoveObject(ctx context.Context, bucketName, fileName string) error {
	if m.removeObjectFn != nil {
		return m.removeObjectFn(ctx, bucketName, fileName)
	}
	return nil
}

func nativeTestMAC(seed byte) objects.MAC {
	var m objects.MAC
	for i := range m {
		m[i] = seed + byte(i)
	}
	return m
}

func TestNewNativeStore_ErrorsWithoutNetwork(t *testing.T) {
	t.Parallel()

	baseConfig := map[string]string{
		"location":          "b2://s3.us-west-004.backblazeb2.com/mybucket/prefix",
		"access_key":        "test-access-key",
		"secret_access_key": "test-secret-key",
	}

	tests := []struct {
		name    string
		mutate  func(map[string]string)
		errLike string
	}{
		{name: "missing access_key", mutate: func(cfg map[string]string) { delete(cfg, "access_key") }, errLike: "missing access_key"},
		{name: "missing secret_access_key", mutate: func(cfg map[string]string) { delete(cfg, "secret_access_key") }, errLike: "missing secret_access_key"},
		{name: "invalid location", mutate: func(cfg map[string]string) { cfg["location"] = "%" }, errLike: "parse location"},
		{name: "missing host", mutate: func(cfg map[string]string) { cfg["location"] = "b2:///mybucket/prefix" }, errLike: "failed to parse the location"},
		{name: "missing bucket", mutate: func(cfg map[string]string) { cfg["location"] = "b2://s3.us-west-004.backblazeb2.com" }, errLike: "failed to parse the location"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := make(map[string]string, len(baseConfig))
			for k, v := range baseConfig {
				cfg[k] = v
			}
			tc.mutate(cfg)

			st, err := NewNativeStore(context.Background(), "b2", cfg)
			if err == nil {
				t.Fatalf("expected error, got store=%#v", st)
			}
			if !strings.Contains(err.Error(), tc.errLike) {
				t.Fatalf("expected error containing %q, got %q", tc.errLike, err.Error())
			}
		})
	}
}

func TestNewNativeStore_AcceptsNativeLocationWithoutEndpoint(t *testing.T) {
	t.Parallel()

	store, err := NewNativeStore(context.Background(), "b2", map[string]string{
		"location":          "b2://mybucket/my/prefix",
		"access_key":        "test-access-key",
		"secret_access_key": "test-secret-key",
	})
	if err != nil {
		t.Fatalf("NewNativeStore() unexpected error: %v", err)
	}

	s, ok := store.(*NativeStore)
	if !ok {
		t.Fatalf("unexpected store type: %T", store)
	}
	if s.bucket != "mybucket" {
		t.Fatalf("bucket: got %q, want %q", s.bucket, "mybucket")
	}
	if got := s.realpath("CONFIG"); got != "my/prefix/CONFIG" {
		t.Fatalf("realpath: got %q, want %q", got, "my/prefix/CONFIG")
	}
}

func TestNewNativeStore_AcceptsLegacyLocationWithEndpoint(t *testing.T) {
	t.Parallel()

	store, err := NewNativeStore(context.Background(), "b2", map[string]string{
		"location":          "b2://s3.us-west-004.backblazeb2.com/mybucket/my/prefix",
		"access_key":        "test-access-key",
		"secret_access_key": "test-secret-key",
	})
	if err != nil {
		t.Fatalf("NewNativeStore() unexpected error: %v", err)
	}

	s, ok := store.(*NativeStore)
	if !ok {
		t.Fatalf("unexpected store type: %T", store)
	}
	if s.bucket != "mybucket" {
		t.Fatalf("bucket: got %q, want %q", s.bucket, "mybucket")
	}
	if got := s.realpath("CONFIG"); got != "my/prefix/CONFIG" {
		t.Fatalf("realpath: got %q, want %q", got, "my/prefix/CONFIG")
	}
}

func TestNativeCreate(t *testing.T) {
	t.Run("creates missing bucket and writes config", func(t *testing.T) {
		made := false
		wrote := false

		m := &mockB2NativeClient{
			bucketExistsFn: func(ctx context.Context, bucketName string) (bool, error) { return false, nil },
			makeBucketFn: func(ctx context.Context, bucketName, bucketType string) (string, error) {
				made = true
				if bucketType != "allPrivate" {
					t.Fatalf("bucketType: got %q, want %q", bucketType, "allPrivate")
				}
				return "b-id", nil
			},
			statObjectFn: func(ctx context.Context, bucketName, fileName string) (*B2FileInfo, error) {
				return nil, ErrB2FileNotFound
			},
			putObjectFn: func(ctx context.Context, bucketName, fileName string, rd io.Reader, contentType string) (int64, error) {
				wrote = true
				if fileName != "repo/CONFIG" {
					t.Fatalf("Put key: got %q, want %q", fileName, "repo/CONFIG")
				}
				data, _ := io.ReadAll(rd)
				if string(data) != "cfg" {
					t.Fatalf("Put payload: got %q, want %q", string(data), "cfg")
				}
				return int64(len(data)), nil
			},
		}

		s := &NativeStore{client: m, bucket: "mybucket", prefixDir: "/repo/"}
		if err := s.Create(context.Background(), []byte("cfg")); err != nil {
			t.Fatalf("Create() unexpected error: %v", err)
		}
		if !made || !wrote {
			t.Fatalf("expected make bucket and put object to be called")
		}
	})

	t.Run("already initialized", func(t *testing.T) {
		m := &mockB2NativeClient{
			bucketExistsFn: func(ctx context.Context, bucketName string) (bool, error) { return true, nil },
			statObjectFn:   func(ctx context.Context, bucketName, fileName string) (*B2FileInfo, error) { return &B2FileInfo{}, nil },
		}
		s := &NativeStore{client: m, bucket: "mybucket", prefixDir: "/repo/"}
		err := s.Create(context.Background(), []byte("cfg"))
		if err == nil || !strings.Contains(err.Error(), "bucket already initialized") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

type closeReadCloser struct {
	reader io.Reader
	closed bool
}

func (r *closeReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r *closeReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestNativeOpenAndPing(t *testing.T) {
	t.Run("open reads and closes", func(t *testing.T) {
		stream := &closeReadCloser{reader: strings.NewReader("config-data")}
		m := &mockB2NativeClient{
			bucketExistsFn: func(ctx context.Context, bucketName string) (bool, error) { return true, nil },
			getObjectFn: func(ctx context.Context, bucketName, fileName string) (io.ReadCloser, error) {
				if fileName != "repo/CONFIG" {
					t.Fatalf("Get key: got %q, want %q", fileName, "repo/CONFIG")
				}
				return stream, nil
			},
		}
		s := &NativeStore{client: m, bucket: "mybucket", prefixDir: "/repo/"}
		data, err := s.Open(context.Background())
		if err != nil {
			t.Fatalf("Open() unexpected error: %v", err)
		}
		if string(data) != "config-data" {
			t.Fatalf("Open() data: got %q", string(data))
		}
		if !stream.closed {
			t.Fatalf("expected stream close")
		}
	})

	t.Run("open missing config maps to fs.ErrNotExist", func(t *testing.T) {
		m := &mockB2NativeClient{
			bucketExistsFn: func(ctx context.Context, bucketName string) (bool, error) { return true, nil },
			getObjectFn: func(ctx context.Context, bucketName, fileName string) (io.ReadCloser, error) {
				return nil, ErrB2FileNotFound
			},
		}
		s := &NativeStore{client: m, bucket: "mybucket", prefixDir: "/repo/"}
		_, err := s.Open(context.Background())
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Open() error: got %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("ping missing bucket", func(t *testing.T) {
		m := &mockB2NativeClient{bucketExistsFn: func(ctx context.Context, bucketName string) (bool, error) { return false, nil }}
		s := &NativeStore{client: m, bucket: "mybucket"}
		err := s.Ping(context.Background())
		if err == nil || !strings.Contains(err.Error(), "bucket does not exist") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestNativeListPutGetDelete(t *testing.T) {
	t.Run("list packfiles", func(t *testing.T) {
		mac := nativeTestMAC(0x10)
		m := &mockB2NativeClient{
			listObjectsFn: func(ctx context.Context, bucketName, prefix string) ([]B2FileInfo, error) {
				return []B2FileInfo{
					{FileName: fmt.Sprintf("repo/packfiles/%02x/%x", mac[0], mac[:])},
					{FileName: "repo/packfiles/aa/abcd"},
				}, nil
			},
		}
		s := &NativeStore{client: m, bucket: "mybucket", prefixDir: "/repo/"}
		list, err := s.List(context.Background(), connectorstorage.StorageResourcePackfile)
		if err != nil {
			t.Fatalf("List() unexpected error: %v", err)
		}
		if len(list) != 1 || list[0] != mac {
			t.Fatalf("unexpected list result: %#v", list)
		}
	})

	t.Run("put/get/delete lock", func(t *testing.T) {
		mac := nativeTestMAC(0x20)
		var stored []byte
		m := &mockB2NativeClient{
			putObjectFn: func(ctx context.Context, bucketName, fileName string, rd io.Reader, contentType string) (int64, error) {
				if fileName != fmt.Sprintf("repo/locks/%016x", mac) {
					t.Fatalf("put key mismatch: %q", fileName)
				}
				stored, _ = io.ReadAll(rd)
				return int64(len(stored)), nil
			},
			getObjectFn: func(ctx context.Context, bucketName, fileName string) (io.ReadCloser, error) {
				if fileName != fmt.Sprintf("repo/locks/%016x", mac) {
					t.Fatalf("get key mismatch: %q", fileName)
				}
				return io.NopCloser(strings.NewReader(string(stored))), nil
			},
			removeObjectFn: func(ctx context.Context, bucketName, fileName string) error {
				if fileName != fmt.Sprintf("repo/locks/%016x", mac) {
					t.Fatalf("delete key mismatch: %q", fileName)
				}
				return nil
			},
		}

		s := &NativeStore{client: m, bucket: "mybucket", prefixDir: "/repo/"}
		n, err := s.Put(context.Background(), connectorstorage.StorageResourceLock, mac, strings.NewReader("lock-data"))
		if err != nil || n != int64(len("lock-data")) {
			t.Fatalf("Put() unexpected result: n=%d err=%v", n, err)
		}

		rc, err := s.Get(context.Background(), connectorstorage.StorageResourceLock, mac, nil)
		if err != nil {
			t.Fatalf("Get() unexpected error: %v", err)
		}
		defer rc.Close()
		data, _ := io.ReadAll(rc)
		if string(data) != "lock-data" {
			t.Fatalf("Get() data mismatch: %q", string(data))
		}

		if err := s.Delete(context.Background(), connectorstorage.StorageResourceLock, mac); err != nil {
			t.Fatalf("Delete() unexpected error: %v", err)
		}
	})

	t.Run("get range", func(t *testing.T) {
		mac := nativeTestMAC(0x30)
		m := &mockB2NativeClient{
			getObjectFn: func(ctx context.Context, bucketName, fileName string) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("abcdefghij")), nil
			},
		}
		s := &NativeStore{client: m, bucket: "mybucket", prefixDir: "/repo/"}
		rc, err := s.Get(context.Background(), connectorstorage.StorageResourceLock, mac, &connectorstorage.Range{Offset: 2, Length: 4})
		if err != nil {
			t.Fatalf("Get(range) unexpected error: %v", err)
		}
		defer rc.Close()
		data, _ := io.ReadAll(rc)
		if string(data) != "cdef" {
			t.Fatalf("Get(range) data mismatch: %q", string(data))
		}
	})
}

func TestNativeMetadataUtils(t *testing.T) {
	t.Parallel()
	s := &NativeStore{host: "host.example", bucket: "mybucket", prefixDir: "/repo/"}
	if s.Origin() != "host.example" {
		t.Fatalf("Origin mismatch")
	}
	if s.Root() != "/mybucket/repo" {
		t.Fatalf("Root mismatch: %q", s.Root())
	}
	if s.Type() != "b2" {
		t.Fatalf("Type mismatch")
	}
	if s.Flags() != location.Flags(0) {
		t.Fatalf("Flags mismatch")
	}
	mode, err := s.Mode(context.Background())
	if err != nil || mode != (connectorstorage.ModeRead|connectorstorage.ModeWrite) {
		t.Fatalf("Mode mismatch: %v %v", mode, err)
	}
	size, err := s.Size(context.Background())
	if err != nil || size != -1 {
		t.Fatalf("Size mismatch: %d %v", size, err)
	}
}

func TestNativeUnsupportedResources(t *testing.T) {
	t.Parallel()
	s := &NativeStore{client: &mockB2NativeClient{}, bucket: "mybucket", prefixDir: "/repo/"}
	mac := nativeTestMAC(0x42)

	if _, err := s.List(context.Background(), connectorstorage.StorageResource(999)); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("List unsupported: %v", err)
	}
	if _, err := s.Put(context.Background(), connectorstorage.StorageResource(999), mac, strings.NewReader("x")); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Put unsupported: %v", err)
	}
	if _, err := s.Get(context.Background(), connectorstorage.StorageResource(999), mac, nil); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Get unsupported: %v", err)
	}
	if err := s.Delete(context.Background(), connectorstorage.StorageResource(999), mac); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Delete unsupported: %v", err)
	}
}
