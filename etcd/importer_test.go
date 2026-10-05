package etcd

import (
	"context"
	"testing"
)

func TestOrigin(t *testing.T) {
	for _, tc := range []struct{ proto, location, want string }{
		{"etcd", "etcd://host:2379", "host:2379"},
		{"etcd+http", "etcd+http://host:2379", "host:2379"},
		{"etcd+https", "etcd+https://host:2379", "host:2379"},
		{"etcd+http", "etcd+http://h", "h"},
		{"etcd+https", "etcd+https://host:2379/prefix", "host:2379"},
	} {
		imp, err := NewImporter(context.Background(), nil, tc.proto,
			map[string]string{"location": tc.location})
		if err != nil {
			t.Errorf("%s: %v", tc.location, err)
			continue
		}
		if got := imp.Origin(); got != tc.want {
			t.Errorf("%s: origin %q, want %q", tc.location, got, tc.want)
		}
		imp.Close(context.Background())
	}
}
