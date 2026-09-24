package common

import (
	"reflect"
	"sort"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
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

// Each case is a message on the server (flags and subject), and the name it
// is stored under in the snapshot. The name must be built as shown, and parsing
// it back must return the same flags.
func TestMessageFileNameFlags(t *testing.T) {
	tests := []struct {
		name     string
		flags    []imap.Flag
		subject  string
		filename string
	}{
		// no flags
		{"no flags, no subject", []imap.Flag{}, "", "42.eml"},
		{"no flags", []imap.Flag{}, "Hello", "42-Hello.eml"},

		// system flags
		{"seen", []imap.Flag{imap.FlagSeen}, "Hello", "42,S-Hello.eml"},
		{"answered", []imap.Flag{imap.FlagAnswered}, "Re: lunch", "42,A-Re__lunch.eml"},
		{"flagged", []imap.Flag{imap.FlagFlagged}, "Invoice", "42,F-Invoice.eml"},
		{"draft", []imap.Flag{imap.FlagDraft}, "", "42,D.eml"},
		{"deleted", []imap.Flag{imap.FlagDeleted}, "Old news", "42,T-Old_news.eml"},
		{"all system flags", []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDraft, imap.FlagDeleted}, "Hello", "42,ADFST-Hello.eml"},
		{"system flags, no subject", []imap.Flag{imap.FlagSeen, imap.FlagAnswered}, "", "42,AS.eml"},

		// standard keywords
		{"forwarded", []imap.Flag{imap.FlagSeen, imap.FlagForwarded}, "Fwd: contract", "42,S{$Forwarded}-Fwd__contract.eml"},
		{"junk", []imap.Flag{imap.FlagJunk}, "You won!", "42,{$Junk}-You_won_.eml"},
		{"not junk", []imap.Flag{imap.FlagNotJunk}, "Newsletter", "42,{$NotJunk}-Newsletter.eml"},
		{"read receipt sent", []imap.Flag{imap.FlagMDNSent}, "Report", "42,{$MDNSent}-Report.eml"},
		{"phishing", []imap.Flag{"$Phishing"}, "Verify your account", "42,{$Phishing}-Verify_your_account.eml"},
		{"Thunderbird tag", []imap.Flag{imap.FlagSeen, "$label1"}, "Board meeting", "42,S{$label1}-Board_meeting.eml"},
		{"Apple Mail flag colour", []imap.Flag{imap.FlagFlagged, "$MailFlagBit0"}, "Urgent", "42,F{$MailFlagBit0}-Urgent.eml"},
		{"several standard keywords", []imap.Flag{imap.FlagForwarded, imap.FlagNotJunk, "$label1"}, "Hello", "42,{$Forwarded}{$NotJunk}{$label1}-Hello.eml"},

		// keywords with a hyphen: the old parser cut these at the first "-"
		{"hyphenated keyword", []imap.Flag{imap.FlagSeen, "follow-up"}, "Customer call", "42,S{follow-up}-Customer_call.eml"},
		{"hyphenated keyword, unread", []imap.Flag{"to-do"}, "Renew domain", "42,{to-do}-Renew_domain.eml"},
		{"hyphenated keyword, no subject", []imap.Flag{"needs-reply"}, "", "42,{needs-reply}.eml"},
		{"leading hyphen", []imap.Flag{"-urgent"}, "Hello", "42,{-urgent}-Hello.eml"},
		{"trailing hyphen", []imap.Flag{"urgent-"}, "Hello", "42,{urgent-}-Hello.eml"},
		{"keyword is only a hyphen", []imap.Flag{"-"}, "Hello", "42,{-}-Hello.eml"},
		{"double hyphen", []imap.Flag{"a--b"}, "Hello", "42,{a--b}-Hello.eml"},
		{"many hyphens", []imap.Flag{"a-b-c-d"}, "Hello", "42,{a-b-c-d}-Hello.eml"},
		{"long hyphenated keyword", []imap.Flag{"project-2026-q3-review"}, "Review", "42,{project-2026-q3-review}-Review.eml"},
		{"several hyphenated keywords", []imap.Flag{"follow-up", "to-do", "needs-reply"}, "Hello", "42,{follow-up}{needs-reply}{to-do}-Hello.eml"},
		{"all system flags and hyphenated keyword", []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDraft, imap.FlagDeleted, "follow-up"}, "Hello", "42,ADFST{follow-up}-Hello.eml"},
		{"standard and hyphenated keywords", []imap.Flag{imap.FlagFlagged, imap.FlagForwarded, "follow-up", "needs-reply"}, "Re: offer", "42,F{$Forwarded}{follow-up}{needs-reply}-Re__offer.eml"},
		{"junk and hyphenated keyword", []imap.Flag{imap.FlagJunk, "to-do"}, "Spam", "42,{$Junk}{to-do}-Spam.eml"},
		{"hyphens in keyword and subject", []imap.Flag{imap.FlagSeen, "follow-up"}, "Re: offer - final", "42,S{follow-up}-Re__offer___final.eml"},

		// keywords that need escaping
		{"keyword with space", []imap.Flag{"big deal"}, "Hello", "42,{big%20deal}-Hello.eml"},
		{"keyword with percent", []imap.Flag{"100%"}, "Sale", "42,{100%25}-Sale.eml"},
		{"keyword with comma", []imap.Flag{"a,b"}, "Hello", "42,{a%2Cb}-Hello.eml"},
		{"keyword with braces", []imap.Flag{"x{y}"}, "Hello", "42,{x%7By%7D}-Hello.eml"},
		{"non-ASCII keyword", []imap.Flag{"café"}, "Menu", "42,{caf%C3%A9}-Menu.eml"},
		{"keyword with space and hyphen", []imap.Flag{"big deal-today"}, "Hello", "42,{big%20deal-today}-Hello.eml"},
		{"keyword with percent and hyphen", []imap.Flag{"50%-off"}, "Sale", "42,{50%25-off}-Sale.eml"},
		{"non-ASCII keyword with hyphen", []imap.Flag{"café-noir"}, "Menu", "42,{caf%C3%A9-noir}-Menu.eml"},
		{"keyword with braces and hyphen", []imap.Flag{"x-{y}"}, "Hello", "42,{x-%7By%7D}-Hello.eml"},

		// subjects that look like flags
		{"subject is a hyphen", []imap.Flag{imap.FlagSeen}, "-", "42,S-_.eml"},
		{"subject starts with hyphens", []imap.Flag{imap.FlagSeen}, "--draft", "42,S-__draft.eml"},
		{"subject with braces", []imap.Flag{imap.FlagSeen}, "{x}", "42,S-_x_.eml"},
		{"subject with comma", []imap.Flag{imap.FlagJunk}, "a, b", "42,{$Junk}-a__b.eml"},
		{"subject with hyphens", []imap.Flag{imap.FlagJunk}, "a-b-c", "42,{$Junk}-a_b_c.eml"},
		{"hyphenated keyword and subject with braces", []imap.Flag{"follow-up"}, "{x}-y", "42,{follow-up}-_x__y.eml"},
		{"hyphenated keyword and subject with comma", []imap.Flag{imap.FlagSeen, "follow-up"}, "has, comma", "42,S{follow-up}-has__comma.eml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.filename, MessageFileName(42, tt.flags, tt.subject))
			assert.ElementsMatch(t, tt.flags, ParseMessageFileName(tt.filename))
		})
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
