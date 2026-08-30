package notion

import (
	"path/filepath"
	"testing"
)

func TestRelativeRejectsEscapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/a/b", filepath.Join("a", "b")},
		{"/", "."},
		{"", "."},
		{"/../../etc/passwd", filepath.Join("etc", "passwd")},
		{"/a/../../b", "b"},
	} {
		got, err := relative(tc.in)
		if err != nil {
			t.Fatalf("relative(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("relative(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCheckAttachmentURL(t *testing.T) {
	ok := []string{
		"https://s3.us-west-2.amazonaws.com/secure.notion-static.com/x/img.png",
		"https://prod-files-secure.s3.amazonaws.com/a/b.jpg",
	}
	for _, u := range ok {
		if err := checkAttachmentURL(u); err != nil {
			t.Errorf("checkAttachmentURL(%q) = %v, want nil", u, err)
		}
	}

	bad := []string{
		"http://example.com/img.png",   // not https
		"file:///etc/passwd",           // not https
		"https://127.0.0.1/admin",      // loopback
		"https://169.254.169.254/meta", // link-local metadata service
		"https://10.0.0.1/internal",    // private
		"https://[::1]/x",              // loopback v6
	}
	for _, u := range bad {
		if err := checkAttachmentURL(u); err == nil {
			t.Errorf("checkAttachmentURL(%q) = nil, want an error", u)
		}
	}
}
