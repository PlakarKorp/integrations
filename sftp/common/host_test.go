package common

import (
	"net/url"
	"testing"
)

// url.Parse accepts a host starting with a dash, which ssh would then parse as
// an option rather than a destination.
func TestCheckHostRejectsOptionLookalikes(t *testing.T) {
	for _, loc := range []string{
		"sftp://-oProxyCommand=touch /tmp/pwn/path",
		"sftp://user@-oProxyCommand=id/p",
		"sftp://-F/tmp/evil.conf/p",
		"sftp://-/p",
	} {
		u, err := url.Parse(loc)
		if err != nil {
			continue
		}
		if err := checkHost(u.Hostname()); err == nil {
			t.Errorf("checkHost(%q) accepted %q", loc, u.Hostname())
		}
	}
}

func TestCheckHostAcceptsRealHosts(t *testing.T) {
	for _, loc := range []string{
		"sftp://example.com/p",
		"sftp://user@example.com:2222/p",
		"sftp://192.0.2.1/p",
		"sftp://[2001:db8::1]:22/p",
	} {
		u, err := url.Parse(loc)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", loc, err)
		}
		if err := checkHost(u.Hostname()); err != nil {
			t.Errorf("checkHost(%q) rejected %q: %v", loc, u.Hostname(), err)
		}
	}
}
