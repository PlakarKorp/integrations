package exporter

import "testing"

func TestContained(t *testing.T) {
	for _, tc := range []struct {
		root, pathname, want string
		wantErr              bool
	}{
		{"/srv/restore", "/a/b", "/srv/restore/a/b", false},
		{"/srv/restore", "/", "/srv/restore", false},
		{"/srv/restore", "/a/../b", "/srv/restore/b", false},
		{"/", "/a", "/a", false},

		// these used to resolve above the root and be written there
		{"/srv/restore", "/../../etc/passwd", "", true},
		{"/srv/restore", "/..", "", true},
		{"/srv/restore", "/../restore-evil/x", "", true},
	} {
		got, err := contained(tc.root, tc.pathname)
		if tc.wantErr {
			if err == nil {
				t.Errorf("contained(%q, %q) = %q, want an error", tc.root, tc.pathname, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("contained(%q, %q): %v", tc.root, tc.pathname, err)
			continue
		}
		if got != tc.want {
			t.Errorf("contained(%q, %q) = %q, want %q", tc.root, tc.pathname, got, tc.want)
		}
	}
}
