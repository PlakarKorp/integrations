package storage_test

import (
	"os"
	"testing"
)

// TestLiveRegistry runs the round trip against a real registry, named by
// OCI_TEST_REGISTRY as <host>[:<port>]. It is plain HTTP unless
// OCI_TEST_TLS is set, in which case its certificate is not verified.
// OCI_TEST_USERNAME and OCI_TEST_PASSWORD, or OCI_TEST_TOKEN, authenticate.
// Deletes need to be enabled on the registry.
//
//	make up
//	OCI_TEST_REGISTRY=localhost:5000 go test ./...
func TestLiveRegistry(t *testing.T) {
	host := os.Getenv("OCI_TEST_REGISTRY")
	if host == "" {
		t.Skip("OCI_TEST_REGISTRY not set")
	}

	cfg := map[string]string{
		"location": "oci://" + host + "/plakar-test/" + randomHex(4),
	}
	if os.Getenv("OCI_TEST_TLS") != "" {
		cfg["tls_insecure_no_verify"] = "true"
	} else {
		cfg["insecure"] = "true"
	}
	for env, key := range map[string]string{
		"OCI_TEST_USERNAME": "username",
		"OCI_TEST_PASSWORD": "password",
		"OCI_TEST_TOKEN":    "token",
	} {
		if v := os.Getenv(env); v != "" {
			cfg[key] = v
		}
	}

	roundTrip(t, open(t, cfg))
}
