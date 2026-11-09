package common

import "testing"

func TestSanitizePathnameEscapesDotSegmentsAnywhere(t *testing.T) {
	tests := []struct {
		key          string
		wantPathname string
		wantUnsafe   bool
	}{
		{".", "/%2e", true},
		{"..", "/%2e%2e", true},
		{"a/.", "/a/%2e", true},
		{"a/..", "/a/%2e%2e", true},
		{"a/../b", "/a/%2e%2e/b", true},
		{"a/file", "/a/file", false},
		{"..foo", "/..foo", false},
		{"a/%2e", "/a/%252e", false},
	}
	for _, tt := range tests {
		pathname, unsafe := SanitizePathname(tt.key)
		if pathname != tt.wantPathname || unsafe != tt.wantUnsafe {
			t.Errorf("SanitizePathname(%q) = (%q, %v), want (%q, %v)",
				tt.key, pathname, unsafe, tt.wantPathname, tt.wantUnsafe)
		}
	}
}

func TestSanitizePathnameRoundTrips(t *testing.T) {
	keys := []string{".", "..", "a/.", "a/..", "a/../b", "a/file", "..foo", "a/%2e", "a/%252e", "100%", "a/%"}
	for _, key := range keys {
		pathname, _ := SanitizePathname(key)
		got := DesanitizePathname(pathname)
		want := "/" + key
		if got != want {
			t.Errorf("round trip for %q: got %q, want %q", key, got, want)
		}
	}
}

// Two different keys must never escape to the same pathname: a legitimate
// "%2e" key must not collide with the escaped form of a ".".
func TestSanitizePathnameNoCollision(t *testing.T) {
	a, _ := SanitizePathname("a/.")
	b, _ := SanitizePathname("a/%2e")
	if a == b {
		t.Fatalf("a/. and a/%%2e both escaped to %q", a)
	}
}
