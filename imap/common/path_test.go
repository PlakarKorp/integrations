package common

import (
	"path"
	"reflect"
	"sort"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMailboxPathRoundTrip(t *testing.T) {
	cases := []struct {
		mailbox string
		delim   rune
		path    string
	}{
		{"INBOX", '/', "/INBOX"},
		{"INBOX", '.', "/INBOX"},
		{"Archive/2024", '/', "/Archive/2024"},
		{"Archive.2024.Q1", '.', "/Archive/2024/Q1"},
		{"Work/Clients/ACME Corp", '/', "/Work/Clients/ACME%20Corp"},
		// A mailbox segment that itself contains the OTHER separator must survive.
		{"Notes/With.Dot", '/', "/Notes/With.Dot"},
		{"Notes.With/Slash", '.', "/Notes/With%2FSlash"},
		{"Flat", 0, "/Flat"},
	}

	for _, c := range cases {
		got := MailboxToPath(c.mailbox, c.delim)
		if got != c.path {
			t.Errorf("MailboxToPath(%q, %q) = %q, want %q", c.mailbox, string(c.delim), got, c.path)
		}
		// Round-trip back using the same delimiter.
		d := c.delim
		if d == 0 {
			d = '/'
		}
		back, err := PathToMailbox(got, d)
		if err != nil {
			t.Fatalf("PathToMailbox(%q): %v", got, err)
		}
		want := c.mailbox
		if c.delim == 0 {
			want = c.mailbox // flat namespace, single segment
		}
		if back != want {
			t.Errorf("round-trip mailbox %q via path %q -> %q, want %q", c.mailbox, got, back, want)
		}
	}
}

func TestMailboxPathDotSegments(t *testing.T) {
	cases := []struct {
		mailbox  string
		path     string
		collides string // mailbox it collapsed onto; "" is the root
	}{
		{"Archive/../Secret", "/Archive/%2E%2E/Secret", "Secret"},
		{"Archive/./Secret", "/Archive/%2E/Secret", "Archive/Secret"},
		{"..", "/%2E%2E", ""},
		{".", "/%2E", ""},
	}

	for _, c := range cases {
		got := MailboxToPath(c.mailbox, '/')
		assert.Equal(t, c.path, got, "MailboxToPath(%q)", c.mailbox)

		// kloset cleans record paths, so the path must survive path.Clean
		// and not land on another mailbox.
		cleaned := path.Clean(got)
		assert.Equal(t, got, cleaned, "MailboxToPath(%q) changed by path.Clean", c.mailbox)
		assert.NotEqual(t, path.Clean(MailboxToPath(c.collides, '/')), cleaned,
			"MailboxToPath(%q) collides with %q", c.mailbox, c.collides)

		back, err := PathToMailbox(cleaned, '/')
		require.NoError(t, err)
		assert.Equal(t, c.mailbox, back, "round-trip via path %q", got)
	}
}

// Paths already stored in snapshots, taken from the encoder before "." and
// ".." were escaped. They must not change, or new backups would diverge from
// old snapshots and restores would decode them to different mailboxes.
func TestMailboxPathStable(t *testing.T) {
	cases := []struct {
		mailbox string
		path    string
	}{
		{"...", "/..."},
		{"..foo", "/..foo"},
		{".hidden", "/.hidden"},
		{"a..b", "/a..b"},
		{"Archive/.../x", "/Archive/.../x"},
		{"100%", "/100%25"},
		{"%2E%2E", "/%252E%252E"},
		{"Été/Café", "/%C3%89t%C3%A9/Caf%C3%A9"},
		{"a?b#c", "/a%3Fb%23c"},
	}

	for _, c := range cases {
		assert.Equal(t, c.path, MailboxToPath(c.mailbox, '/'), "MailboxToPath(%q)", c.mailbox)

		back, err := PathToMailbox(c.path, '/')
		require.NoError(t, err)
		assert.Equal(t, c.mailbox, back, "PathToMailbox(%q)", c.path)
	}
}

func TestPathToMailboxRoot(t *testing.T) {
	for _, p := range []string{"", "/", "//"} {
		mb, err := PathToMailbox(p, '/')
		if err != nil {
			t.Fatalf("PathToMailbox(%q): %v", p, err)
		}
		if mb != "" {
			t.Errorf("PathToMailbox(%q) = %q, want empty", p, mb)
		}
	}
}

func TestFlagRoundTrip(t *testing.T) {
	cases := [][]imap.Flag{
		nil,
		{imap.FlagSeen},
		{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDraft, imap.FlagDeleted},
		{imap.Flag("$Important"), imap.FlagSeen},
		{imap.Flag("Custom Keyword")},
		{imap.Flag(`\Recent`), imap.FlagSeen}, // \Recent must be dropped
	}

	for _, in := range cases {
		block := EncodeFlags(in)
		got := DecodeFlags(block)

		want := make([]imap.Flag, 0, len(in))
		for _, f := range in {
			if f == imap.Flag(`\Recent`) {
				continue
			}
			want = append(want, f)
		}

		sortFlags(got)
		sortFlags(want)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("flags %v -> block %q -> %v, want %v", in, block, got, want)
		}
	}
}

func TestMessageFileNameRoundTrip(t *testing.T) {
	flags := []imap.Flag{imap.FlagSeen, imap.FlagFlagged, imap.Flag("$Junk")}
	name := MessageFileName(imap.UID(42), flags, "Re: Hello, World! / Q&A")

	if name[len(name)-4:] != ".eml" {
		t.Fatalf("name %q does not end in .eml", name)
	}

	got := ParseMessageFileName(name)
	sortFlags(got)
	want := append([]imap.Flag(nil), flags...)
	sortFlags(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseMessageFileName(%q) = %v, want %v", name, got, want)
	}
}

func TestMessageFileNameNoFlags(t *testing.T) {
	name := MessageFileName(imap.UID(7), nil, "")
	if name != "7.eml" {
		t.Errorf("MessageFileName(7, nil, \"\") = %q, want 7.eml", name)
	}
	if got := ParseMessageFileName(name); len(got) != 0 {
		t.Errorf("ParseMessageFileName(%q) = %v, want none", name, got)
	}
}

func sortFlags(f []imap.Flag) {
	sort.Slice(f, func(i, j int) bool { return f[i] < f[j] })
}
