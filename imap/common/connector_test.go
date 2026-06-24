package common

import (
	"reflect"
	"testing"
)

func TestInitFromConfigMailboxPrefix(t *testing.T) {
	cases := []struct {
		location string
		want     []string
		addr     string
	}{
		{"imap://mail.example.com", nil, "mail.example.com:143"},
		{"imap://mail.example.com/", nil, "mail.example.com:143"},
		{"imap://mail.example.com/Archive", []string{"Archive"}, "mail.example.com:143"},
		{"imap://mail.example.com/Archive/2024", []string{"Archive", "2024"}, "mail.example.com:143"},
		{"imap://mail.example.com:1143/Work%20Stuff", []string{"Work Stuff"}, "mail.example.com:1143"},
		// A mailbox name that contains a slash, percent-encoded in the URL.
		{"imap://mail.example.com/Notes%2FSub", []string{"Notes/Sub"}, "mail.example.com:143"},
	}
	for _, c := range cases {
		var ic ImapConnector
		err := ic.InitFromConfig(map[string]string{
			"location": c.location,
			"username": "u",
			"password": "p",
			"tls":      "no-tls",
		})
		if err != nil {
			t.Fatalf("InitFromConfig(%q): %v", c.location, err)
		}
		if !reflect.DeepEqual(ic.MailboxPrefix, c.want) {
			t.Errorf("location %q -> prefix %v, want %v", c.location, ic.MailboxPrefix, c.want)
		}
		if ic.Address != c.addr {
			t.Errorf("location %q -> address %q, want %q", c.location, ic.Address, c.addr)
		}
	}
}

func TestSegmentsToMailboxAndPath(t *testing.T) {
	segs := []string{"Archive", "2024"}

	if got := SegmentsToMailbox(segs, '.'); got != "Archive.2024" {
		t.Errorf("SegmentsToMailbox(., ) = %q, want Archive.2024", got)
	}
	if got := SegmentsToMailbox(segs, '/'); got != "Archive/2024" {
		t.Errorf("SegmentsToMailbox(/, ) = %q, want Archive/2024", got)
	}
	if got := SegmentsToMailbox(nil, '.'); got != "" {
		t.Errorf("SegmentsToMailbox(nil) = %q, want empty", got)
	}
	if got := SegmentsToPath(segs); got != "/Archive/2024" {
		t.Errorf("SegmentsToPath = %q, want /Archive/2024", got)
	}
	// A segment with a slash must be percent-encoded in the kloset path.
	if got := SegmentsToPath([]string{"Notes/Sub"}); got != "/Notes%2FSub" {
		t.Errorf("SegmentsToPath(slash) = %q, want /Notes%%2FSub", got)
	}
}
