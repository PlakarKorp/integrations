package etcd

import (
	"strings"
	"testing"
)

// The origin used to be sliced at len(proto)+3, an offset computed against the
// original scheme after the location had already been rewritten to a different
// one.  etcd+https://host:2379 gave "2379", and a short host panicked.
func TestOriginExtraction(t *testing.T) {
	origin := func(location string) string {
		o := location
		if _, rest, found := strings.Cut(o, "://"); found {
			o = rest
		}
		o, _, _ = strings.Cut(o, "/")
		return o
	}

	for _, tc := range []struct{ location, want string }{
		{"https://host:2379", "host:2379"},
		{"http://host:2379", "host:2379"},
		{"https://h", "h"},
		{"http://h", "h"},
		{"https://host:2379/prefix", "host:2379"},
		{"host:2379", "host:2379"},
	} {
		if got := origin(tc.location); got != tc.want {
			t.Errorf("origin(%q) = %q, want %q", tc.location, got, tc.want)
		}
	}
}

func TestEtcdTLS(t *testing.T) {
	// Nothing configured: leave the clientv3 default in place.
	cfg, err := etcdTLS(map[string]string{})
	if err != nil || cfg != nil {
		t.Errorf("etcdTLS({}) = %v, %v; want nil, nil", cfg, err)
	}

	cfg, err = etcdTLS(map[string]string{"tls_insecure_no_verify": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.InsecureSkipVerify {
		t.Error("tls_insecure_no_verify=true did not disable verification")
	}

	if _, err := etcdTLS(map[string]string{"tls_insecure_no_verify": "maybe"}); err == nil {
		t.Error("invalid tls_insecure_no_verify accepted")
	}

	if _, err := etcdTLS(map[string]string{"cert_file": "/nonexistent"}); err == nil {
		t.Error("cert_file without key_file accepted")
	}

	if _, err := etcdTLS(map[string]string{"ca_file": "/nonexistent"}); err == nil {
		t.Error("unreadable ca_file accepted")
	}
}
