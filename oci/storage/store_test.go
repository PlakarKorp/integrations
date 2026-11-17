package storage_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/oci/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRepo = "plakar/repo"

var resources = []integration.StorageResource{
	integration.StorageResourcePackfile,
	integration.StorageResourceState,
	integration.StorageResourceLock,
	integration.StorageResourceECCPackfile,
	integration.StorageResourceECCState,
}

func open(t *testing.T, cfg map[string]string) integration.Store {
	t.Helper()
	st, err := storage.New(context.Background(), "oci", cfg)
	require.NoError(t, err)
	return st
}

func readAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	return data
}

func list(t *testing.T, st integration.Store, res integration.StorageResource) []integration.MAC {
	t.Helper()
	var out []integration.MAC
	for e, err := range st.List(context.Background(), res, 0) {
		require.NoError(t, err)
		out = append(out, e.MAC)
	}
	return out
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

// roundTrip takes a fresh store through the whole Store interface.
func roundTrip(t *testing.T, st integration.Store) {
	t.Helper()
	ctx := context.Background()

	require.NoError(t, st.Ping(ctx))

	config := []byte(`{"repository_id":"test"}`)
	require.NoError(t, st.Create(ctx, config))
	require.Error(t, st.Create(ctx, config), "a second Create must refuse the initialized repository")

	got, err := st.Open(ctx)
	require.NoError(t, err)
	assert.Equal(t, config, got)

	mode, err := st.Mode(ctx)
	require.NoError(t, err)
	assert.Equal(t, integration.ModeRead|integration.ModeWrite, mode)

	payload := randomBytes(t, 100<<10)
	for _, res := range resources {
		t.Run(res.String(), func(t *testing.T) {
			mac := integration.RandomMAC()

			n, err := st.Put(ctx, res, 0, mac, bytes.NewReader(payload))
			require.NoError(t, err)
			assert.Equal(t, int64(len(payload)), n)

			rc, err := st.Get(ctx, res, 0, mac, nil)
			require.NoError(t, err)
			assert.Equal(t, payload, readAll(t, rc))

			rc, err = st.Get(ctx, res, 0, mac, &integration.Range{Offset: 1000, Length: 4096})
			require.NoError(t, err)
			assert.Equal(t, payload[1000:5096], readAll(t, rc))

			rc, err = st.Get(ctx, res, 0, mac, &integration.Range{Offset: uint64(len(payload) - 10), Length: 10})
			require.NoError(t, err)
			assert.Equal(t, payload[len(payload)-10:], readAll(t, rc))

			rc, err = st.Get(ctx, res, 0, mac, &integration.Range{Offset: 5, Length: 0})
			require.NoError(t, err)
			assert.Empty(t, readAll(t, rc))

			assert.Equal(t, []integration.MAC{mac}, list(t, st, res))

			require.NoError(t, st.Delete(ctx, res, 0, mac))
			assert.Empty(t, list(t, st, res))

			_, err = st.Get(ctx, res, 0, mac, nil)
			require.Error(t, err, "a deleted object must be gone")
		})
	}

	_, err = st.Get(ctx, integration.StorageResourcePackfile, 0, integration.RandomMAC(), nil)
	require.Error(t, err, "an unknown object must not be found")

	require.NoError(t, st.Close(ctx))
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string]fakeOptions{
		"untag":                      {tagDelete: true},
		"delete by digest":           {tagDelete: false},
		"delete by digest after 405": {tagDelete: false, untag405: true},
		"tls":              {tagDelete: true, tls: true},
		"blobs on a CDN":   {tagDelete: true, redirectBlobs: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFake(t, opts)
			st := open(t, f.config(testRepo))
			assert.Equal(t, f.host(), st.Origin())
			assert.Equal(t, "/"+testRepo, st.Root())
			assert.Equal(t, "oci", st.Type())
			roundTrip(t, st)
		})
	}
}

func TestOpenWithoutConfig(t *testing.T) {
	t.Parallel()
	f := newFake(t, fakeOptions{})
	st := open(t, f.config(testRepo))
	_, err := st.Open(context.Background())
	require.Error(t, err)
}

func TestListPaginates(t *testing.T) {
	t.Parallel()
	f := newFake(t, fakeOptions{pageSize: 3, tagDelete: true})
	st := open(t, f.config(testRepo))
	ctx := context.Background()
	require.NoError(t, st.Create(ctx, []byte("config")))

	want := map[integration.MAC]bool{}
	for range 7 {
		mac := integration.RandomMAC()
		want[mac] = true
		_, err := st.Put(ctx, integration.StorageResourcePackfile, 0, mac, bytes.NewReader([]byte("packfile")))
		require.NoError(t, err)
	}
	for range 2 {
		_, err := st.Put(ctx, integration.StorageResourceState, 0, integration.RandomMAC(), bytes.NewReader([]byte("state")))
		require.NoError(t, err)
	}

	got := map[integration.MAC]bool{}
	for _, mac := range list(t, st, integration.StorageResourcePackfile) {
		got[mac] = true
	}
	assert.Equal(t, want, got)
	// 10 tags with CONFIG, 3 per page.
	assert.Len(t, f.requests(http.MethodGet, "/tags/list"), 4)
}

func TestListStopsEarly(t *testing.T) {
	t.Parallel()
	f := newFake(t, fakeOptions{pageSize: 2})
	st := open(t, f.config(testRepo))
	ctx := context.Background()
	require.NoError(t, st.Create(ctx, []byte("config")))
	for range 6 {
		_, err := st.Put(ctx, integration.StorageResourcePackfile, 0, integration.RandomMAC(), bytes.NewReader([]byte("packfile")))
		require.NoError(t, err)
	}

	var n int
	for _, err := range st.List(ctx, integration.StorageResourcePackfile, 0) {
		require.NoError(t, err)
		n++
		if n == 1 {
			break
		}
	}
	assert.Len(t, f.requests(http.MethodGet, "/tags/list"), 1, "breaking out of List must stop the listing")
}

func TestGetRangeWhenRegistryIgnoresRange(t *testing.T) {
	t.Parallel()
	f := newFake(t, fakeOptions{ignoreRange: true})
	st := open(t, f.config(testRepo))
	ctx := context.Background()

	payload := randomBytes(t, 64<<10)
	mac := integration.RandomMAC()
	_, err := st.Put(ctx, integration.StorageResourcePackfile, 0, mac, bytes.NewReader(payload))
	require.NoError(t, err)

	rc, err := st.Get(ctx, integration.StorageResourcePackfile, 0, mac, &integration.Range{Offset: 12345, Length: 777})
	require.NoError(t, err)
	assert.Equal(t, payload[12345:12345+777], readAll(t, rc))
}

func TestIdenticalObjectsKeepDistinctManifests(t *testing.T) {
	t.Parallel()
	f := newFake(t, fakeOptions{tagDelete: false})
	st := open(t, f.config(testRepo))
	ctx := context.Background()

	a, b := integration.RandomMAC(), integration.RandomMAC()
	for _, mac := range []integration.MAC{a, b} {
		_, err := st.Put(ctx, integration.StorageResourceLock, 0, mac, bytes.NewReader([]byte("same lock")))
		require.NoError(t, err)
	}

	require.NoError(t, st.Delete(ctx, integration.StorageResourceLock, 0, a))
	assert.Equal(t, []integration.MAC{b}, list(t, st, integration.StorageResourceLock), "deleting a lock by digest must not take its twin")

	rc, err := st.Get(ctx, integration.StorageResourceLock, 0, b, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("same lock"), readAll(t, rc))
}

func TestEmptyConfigBlobUploadedOnce(t *testing.T) {
	t.Parallel()
	f := newFake(t, fakeOptions{})
	st := open(t, f.config(testRepo))
	ctx := context.Background()
	for range 3 {
		_, err := st.Put(ctx, integration.StorageResourcePackfile, 0, integration.RandomMAC(), bytes.NewReader([]byte("packfile")))
		require.NoError(t, err)
	}
	// One upload per packfile, plus one for the shared config blob.
	assert.Len(t, f.requests(http.MethodPost, "/blobs/uploads/"), 4)
}

func TestBasicAuth(t *testing.T) {
	t.Parallel()
	f := newFake(t, fakeOptions{basicUser: "plakar", basicPass: "s3cret", tagDelete: true, redirectBlobs: true})

	t.Run("without credentials", func(t *testing.T) {
		st := open(t, f.config("anonymous/repo"))
		require.Error(t, st.Create(context.Background(), []byte("config")))
	})

	t.Run("wrong password", func(t *testing.T) {
		cfg := f.config("wrong/repo")
		cfg["username"] = "plakar"
		cfg["password"] = "nope"
		st := open(t, cfg)
		require.Error(t, st.Create(context.Background(), []byte("config")))
	})

	t.Run("with credentials", func(t *testing.T) {
		cfg := f.config(testRepo)
		cfg["username"] = "plakar"
		cfg["password"] = "s3cret"
		roundTrip(t, open(t, cfg))

		mirrored := f.mirrorRequests()
		require.NotEmpty(t, mirrored, "blob downloads must have followed the redirect")
		for _, r := range mirrored {
			assert.Empty(t, r.auth, "credentials must not follow a redirect off the registry: %s %s", r.method, r.path)
		}
	})
}

func TestTokenAuth(t *testing.T) {
	t.Parallel()

	t.Run("with credentials", func(t *testing.T) {
		t.Parallel()
		f := newFake(t, fakeOptions{tokenAuth: true, basicUser: "plakar", basicPass: "s3cret", tagDelete: true})
		cfg := f.config(testRepo)
		cfg["username"] = "plakar"
		cfg["password"] = "s3cret"
		st := open(t, cfg)
		roundTrip(t, st)

		// One token per scope the registry asked for, not one per request.
		assert.LessOrEqual(t, f.tokenExchanges(), 4)

		// The credentials go to the realm; the registry gets the tokens.
		for _, r := range f.requests(http.MethodGet, "/token") {
			assert.True(t, strings.HasPrefix(r.auth, "Basic "), "the realm must get the credentials, got %q", r.auth)
		}
		var bearer int
		for _, r := range f.requests("", "/v2/"+testRepo+"/") {
			if strings.HasPrefix(r.auth, "Bearer ") {
				bearer++
			}
		}
		assert.Positive(t, bearer, "registry requests must carry the token")

		before := f.tokenExchanges()
		for range 3 {
			require.NoError(t, st.Ping(context.Background()))
		}
		assert.Equal(t, before, f.tokenExchanges(), "a cached token must be reused")
	})

	t.Run("wrong credentials", func(t *testing.T) {
		t.Parallel()
		f := newFake(t, fakeOptions{tokenAuth: true, basicUser: "plakar", basicPass: "s3cret"})
		cfg := f.config(testRepo)
		cfg["username"] = "plakar"
		cfg["password"] = "nope"
		st := open(t, cfg)
		require.Error(t, st.Create(context.Background(), []byte("config")))
	})

	t.Run("anonymous", func(t *testing.T) {
		t.Parallel()
		f := newFake(t, fakeOptions{tokenAuth: true, tagDelete: true})
		roundTrip(t, open(t, f.config(testRepo)))
		assert.Positive(t, f.tokenExchanges())
	})
}

func TestStaticToken(t *testing.T) {
	t.Parallel()
	f := newFake(t, fakeOptions{staticToken: "tok-123", tagDelete: true})

	st := open(t, f.config("anonymous/repo"))
	require.Error(t, st.Create(context.Background(), []byte("config")))

	cfg := f.config(testRepo)
	cfg["token"] = "tok-123"
	roundTrip(t, open(t, cfg))
}

func TestTokenRealmMustNotBeCleartext(t *testing.T) {
	t.Parallel()
	leak := newFake(t, fakeOptions{})
	f := newFake(t, fakeOptions{tls: true, tokenAuth: true, realm: leak.srv.URL + "/token"})

	cfg := f.config(testRepo)
	cfg["username"] = "plakar"
	cfg["password"] = "s3cret"
	st := open(t, cfg)
	require.Error(t, st.Create(context.Background(), []byte("config")))
	assert.Empty(t, leak.requests("", "/token"), "credentials were sent to a cleartext realm")
}

func TestUploadRedirectRefused(t *testing.T) {
	t.Parallel()
	f := newFake(t, fakeOptions{redirectUploads: true})
	st := open(t, f.config(testRepo))
	_, err := st.Put(context.Background(), integration.StorageResourcePackfile, 0, integration.RandomMAC(), bytes.NewReader([]byte("packfile")))
	require.Error(t, err)
	assert.Empty(t, f.mirrorRequests(), "an upload followed a redirect off the registry")
}

func TestNewRejectsBadConfig(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]map[string]string{
		"no repository":           {"location": "oci://localhost:5000"},
		"no host":                 {"location": "oci:///repo"},
		"credentials in location": {"location": "oci://user:pass@localhost:5000/repo"},
		"query":                   {"location": "oci://localhost:5000/repo?x=1"},
		"invalid repository":      {"location": "oci://localhost:5000/Repo"},
		"password without user":   {"location": "oci://localhost:5000/repo", "password": "x"},
		"token with user":         {"location": "oci://localhost:5000/repo", "token": "x", "username": "u"},
		"insecure and no verify":  {"location": "oci://localhost:5000/repo", "insecure": "true", "tls_insecure_no_verify": "true"},
		"bad bool":                {"location": "oci://localhost:5000/repo", "insecure": "maybe"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := storage.New(context.Background(), "oci", cfg)
			require.Error(t, err)
		})
	}
}
