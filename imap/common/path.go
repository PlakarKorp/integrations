package common

import (
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// The kloset snapshot namespace is always a "/"-separated POSIX-like path.
// IMAP mailbox hierarchies, however, use a server-defined delimiter (commonly
// "." for Dovecot's default mdbox/maildir layout, or "/") and individual
// mailbox names may themselves contain "/" or other characters that would
// otherwise collide with the kloset path separator.
//
// To round-trip a mailbox hierarchy losslessly we:
//   - split the server mailbox name on its delimiter into segments,
//   - percent-encode each segment,
//   - join the encoded segments with "/" to form the kloset directory path.
//
// Decoding reverses this exactly, so "Archive.2024/Q1" under a "." delimiter
// becomes "/Archive/2024%2FQ1" in the snapshot and is restored to the original
// mailbox name regardless of the destination server's delimiter.

// encodeSegment percent-encodes a single mailbox path segment so it never
// contains "/" or other characters that break the kloset path layout.
func encodeSegment(s string) string {
	return url.PathEscape(s)
}

func decodeSegment(s string) (string, error) {
	return url.PathUnescape(s)
}

// MailboxToPath converts an IMAP mailbox name (using the server delimiter) into
// an absolute "/"-separated kloset directory path.
func MailboxToPath(mailbox string, delim rune) string {
	segs := splitMailbox(mailbox, delim)
	for i, s := range segs {
		segs[i] = encodeSegment(s)
	}
	return "/" + strings.Join(segs, "/")
}

// PathToMailbox converts an absolute kloset directory path back into an IMAP
// mailbox name using the destination server delimiter. The leading "/" is
// dropped; an empty path or "/" yields "".
func PathToMailbox(p string, delim rune) (string, error) {
	p = strings.Trim(path.Clean("/"+strings.TrimPrefix(p, "/")), "/")
	if p == "" {
		return "", nil
	}
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		seg, err := decodeSegment(part)
		if err != nil {
			return "", fmt.Errorf("invalid mailbox path segment %q: %w", part, err)
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return "", nil
	}
	d := delim
	if d == 0 {
		d = '/'
	}
	return strings.Join(out, string(d)), nil
}

// splitMailbox splits a mailbox name on the server delimiter. A zero delimiter
// (server returned NIL, i.e. a flat namespace) yields a single segment.
func splitMailbox(mailbox string, delim rune) []string {
	if delim == 0 {
		return []string{mailbox}
	}
	return strings.Split(mailbox, string(delim))
}

// flag round-tripping --------------------------------------------------------
//
// kloset's FileInfo has no place for IMAP flags, so we encode them into the
// message file name, Maildir-style. A backed-up message file is named:
//
//	<uid>[,<flagtoken><flagtoken>...][-<safe-subject>].eml
//
// where each flag token is a single character for the well-known system flags
// and a percent-encoded "{keyword}" for everything else. The exporter parses
// the flag block back out and applies it on APPEND.

var systemFlagToToken = map[imap.Flag]byte{
	imap.FlagSeen:     'S',
	imap.FlagAnswered: 'A',
	imap.FlagFlagged:  'F',
	imap.FlagDraft:    'D',
	imap.FlagDeleted:  'T',
}

var tokenToSystemFlag = func() map[byte]imap.Flag {
	m := make(map[byte]imap.Flag, len(systemFlagToToken))
	for f, t := range systemFlagToToken {
		m[t] = f
	}
	return m
}()

// EncodeFlags renders a set of flags into the filename flag block (the part
// after the comma, before the optional "-subject"). Returns "" when there are
// no flags worth persisting.
func EncodeFlags(flags []imap.Flag) string {
	var sys []byte
	var kw []string
	for _, f := range flags {
		if f == imap.Flag(`\Recent`) {
			// \Recent is session state, never settable via APPEND.
			continue
		}
		if t, ok := systemFlagToToken[f]; ok {
			sys = append(sys, t)
			continue
		}
		kw = append(kw, "{"+url.PathEscape(string(f))+"}")
	}
	sort.Slice(sys, func(i, j int) bool { return sys[i] < sys[j] })
	sort.Strings(kw)
	return string(sys) + strings.Join(kw, "")
}

// DecodeFlags parses a filename flag block back into IMAP flags.
func DecodeFlags(block string) []imap.Flag {
	var flags []imap.Flag
	i := 0
	for i < len(block) {
		c := block[i]
		if c == '{' {
			end := strings.IndexByte(block[i:], '}')
			if end < 0 {
				break
			}
			raw := block[i+1 : i+end]
			if dec, err := url.PathUnescape(raw); err == nil {
				flags = append(flags, imap.Flag(dec))
			}
			i += end + 1
			continue
		}
		if f, ok := tokenToSystemFlag[c]; ok {
			flags = append(flags, f)
		}
		i++
	}
	return flags
}

// MessageFileName builds the kloset file name for a message.
func MessageFileName(uid imap.UID, flags []imap.Flag, subject string) string {
	name := fmt.Sprint(uint32(uid))
	if block := EncodeFlags(flags); block != "" {
		name += "," + block
	}
	if subject != "" {
		name += "-" + SafeName(subject)
	}
	return name + ".eml"
}

// ParseMessageFileName extracts the flag set from a message file name produced
// by MessageFileName. Unknown names simply yield no flags.
func ParseMessageFileName(name string) (flags []imap.Flag) {
	base := strings.TrimSuffix(name, ".eml")
	// strip "-subject"
	if i := strings.IndexByte(base, '-'); i >= 0 {
		base = base[:i]
	}
	comma := strings.IndexByte(base, ',')
	if comma < 0 {
		return nil
	}
	return DecodeFlags(base[comma+1:])
}
