package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeTagsList(t *testing.T) {
	t.Parallel()
	collect := func(body string, limit int) ([]string, error) {
		var tags []string
		err := decodeTagsList(strings.NewReader(body), func(tag string) bool {
			tags = append(tags, tag)
			return limit == 0 || len(tags) < limit
		})
		return tags, err
	}

	tags, err := collect(`{"name":"repo","tags":["a","b","c"]}`, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, tags)

	tags, err = collect(`{"tags":["a","b"],"name":"repo","extra":{"nested":[1,2]}}`, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, tags)

	tags, err = collect(`{"name":"repo","tags":null}`, 0)
	require.NoError(t, err)
	assert.Empty(t, tags)

	tags, err = collect(`{"name":"repo"}`, 0)
	require.NoError(t, err)
	assert.Empty(t, tags)

	tags, err = collect(`{"tags":["a","b","c"]}`, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, tags, "stopping early must stop")

	_, err = collect(`["a","b"]`, 0)
	require.Error(t, err)
	_, err = collect(`{"tags":"a"}`, 0)
	require.Error(t, err)
	_, err = collect(`{"tags":[1]}`, 0)
	require.Error(t, err)
}

func TestParseTokenResponse(t *testing.T) {
	t.Parallel()

	value, ttl, err := parseTokenResponse(strings.NewReader(`{"token":"t","expires_in":300}`))
	require.NoError(t, err)
	assert.Equal(t, "t", value)
	assert.Equal(t, 300*time.Second, ttl)

	value, ttl, err = parseTokenResponse(strings.NewReader(`{"access_token":"a"}`))
	require.NoError(t, err)
	assert.Equal(t, "a", value)
	assert.Equal(t, 60*time.Second, ttl, "no expiry means the spec's minimum")

	value, _, err = parseTokenResponse(strings.NewReader(`{"token":"","access_token":"a"}`))
	require.NoError(t, err)
	assert.Equal(t, "a", value)

	_, _, err = parseTokenResponse(strings.NewReader(`{"Token":"t"}`))
	require.Error(t, err, "keys match exactly")
	_, _, err = parseTokenResponse(strings.NewReader(`{"token":""}`))
	require.Error(t, err)
	_, _, err = parseTokenResponse(strings.NewReader(`{"token":1}`))
	require.Error(t, err)
	_, _, err = parseTokenResponse(strings.NewReader(`[]`))
	require.Error(t, err)
}

func TestParseBearerChallenge(t *testing.T) {
	t.Parallel()
	h := map[string][]string{
		"Www-Authenticate": {
			`Basic realm="registry"`,
			`Bearer realm="https://auth.example.com/token",service="registry.example.com",scope="repository:a/b:pull,push"`,
		},
	}
	ch, ok := parseBearerChallenge(h)
	require.True(t, ok)
	assert.Equal(t, "https://auth.example.com/token", ch.realm)
	assert.Equal(t, "registry.example.com", ch.service)
	assert.Equal(t, "repository:a/b:pull,push", ch.scope)

	_, ok = parseBearerChallenge(map[string][]string{"Www-Authenticate": {`Basic realm="registry"`}})
	assert.False(t, ok)
	_, ok = parseBearerChallenge(map[string][]string{"Www-Authenticate": {`Bearer service="x"`}})
	assert.False(t, ok, "a challenge without a realm is no challenge")

	assert.Equal(t, map[string]string{"realm": "r", "k": "v"}, parseAuthParams(`realm=r, k="v"`))
	assert.Equal(t, map[string]string{"a": ""}, parseAuthParams(`a=`))
	assert.Empty(t, parseAuthParams(`garbage`))
}

func TestParseScope(t *testing.T) {
	t.Parallel()
	repo, actions := parseScope("repository:a/b:pull,push")
	assert.Equal(t, "a/b", repo)
	assert.Equal(t, []string{"pull", "push"}, actions)

	repo, actions = parseScope("repository:localhost:5000/x:pull")
	assert.Equal(t, "localhost:5000/x", repo)
	assert.Equal(t, []string{"pull"}, actions)

	repo, _ = parseScope("registry:catalog:*")
	assert.Empty(t, repo)
	repo, _ = parseScope("")
	assert.Empty(t, repo)
}

func TestHostnameOf(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "registry.example.com", hostnameOf("registry.example.com:5000"))
	assert.Equal(t, "registry.example.com", hostnameOf("registry.example.com"))
	assert.Equal(t, "::1", hostnameOf("[::1]:5000"))
	assert.Equal(t, "::1", hostnameOf("[::1]"))
}
