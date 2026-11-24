package storage

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRealmAllowed(t *testing.T) {
	t.Parallel()
	allowed := func(host, realmHost string, insecure bool, realm string) bool {
		a := &authenticator{host: host, realmHost: realmHost, insecure: insecure}
		u, err := url.Parse(realm)
		if err != nil {
			t.Fatal(err)
		}
		return a.realmAllowed(u)
	}

	assert.True(t, allowed("ghcr.io", "", false, "https://ghcr.io/token"))
	assert.True(t, allowed("GHCR.IO", "", false, "https://ghcr.io/token"))
	assert.True(t, allowed("registry.example.com:5000", "", false, "https://registry.example.com/v2/token"), "another port of the registry host")
	assert.True(t, allowed("registry-1.docker.io", "", false, "https://auth.docker.io/token"))
	assert.True(t, allowed("index.docker.io", "", false, "https://auth.docker.io/token"))
	assert.True(t, allowed("registry.example.com", "auth.example.com", false, "https://auth.example.com/token"))
	assert.True(t, allowed("localhost:5000", "", true, "http://localhost:5001/token"), "cleartext on the registry host of a cleartext registry")
	assert.True(t, allowed("localhost:5000", "", true, "https://localhost:5001/token"))

	assert.False(t, allowed("ghcr.io", "", false, "https://evil.example.com/token"))
	assert.False(t, allowed("ghcr.io", "", false, "http://ghcr.io/token"), "cleartext realm of a TLS registry")
	assert.False(t, allowed("ghcr.io", "", true, "http://169.254.169.254/latest/meta-data/"), "another cleartext host")
	assert.False(t, allowed("registry-1.docker.io", "", false, "http://auth.docker.io/token"))
	assert.False(t, allowed("ghcr.io", "", false, "https://auth.docker.io/token"), "Docker Hub's realm is for Docker Hub")
	assert.False(t, allowed("registry.example.com", "auth.example.com", false, "https://other.example.com/token"))
	assert.False(t, allowed("ghcr.io", "", false, "ftp://ghcr.io/token"))
	assert.False(t, allowed("ghcr.io", "", false, "https:///token"))
}
