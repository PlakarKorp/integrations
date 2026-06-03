// Package store implements a kloset storage.Store backed by an IMAP server.
//
// A kloset repository is a content-addressed blob store: it Puts/Gets/Lists/
// Deletes immutable blobs keyed by a 32-byte MAC, grouped by resource type
// (packfile, state, lock, config, and their ECC variants). This backend maps
// that model onto IMAP mailboxes:
//
//   - one mailbox per resource type under a configurable root (default
//     "Plakar"), e.g. Plakar/packfile, Plakar/state, Plakar/lock, ...
//   - one e-mail message per blob; the MAC is stored in an X-Plakar-Mac
//     header and the blob bytes are base64-encoded into the message body.
//
// This is deliberately an abuse of IMAP: it works against a faithful server
// (verified on Dovecot) but inherits IMAP's caveats — no atomic
// create-if-absent (locks are best-effort), per-message size limits, and a
// round-trip per operation. It is not meant to compete with the fs/s3/sftp
// stores on performance.
package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/PlakarKorp/integrations/imap/common"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/emersion/go-imap/v2"
)

const (
	macHeader   = "X-Plakar-Mac"
	defaultRoot = "Plakar"
	// configMAC is the well-known key under which the repository configuration
	// blob is stored in the config mailbox.
	configName = "config"
)

func init() {
	storage.Register("imap", 0, NewStore)
}

// resourceMailbox maps a storage resource to its leaf mailbox name.
func resourceMailbox(res storage.StorageResource) (string, bool) {
	switch res {
	case storage.StorageResourcePackfile:
		return "packfile", true
	case storage.StorageResourceState:
		return "state", true
	case storage.StorageResourceLock:
		return "lock", true
	case storage.StorageResourceECCPackfile:
		return "eccpackfile", true
	case storage.StorageResourceECCState:
		return "eccstate", true
	default:
		return "", false
	}
}

type Store struct {
	connector common.ImapConnector
	location  string
	root      string
	delim     rune

	mu       sync.Mutex
	session  *common.ImapSession
	selected string
	// index caches MAC -> UID per fully-qualified mailbox so Get/Delete avoid a
	// SEARCH round-trip. It is rebuilt lazily and on List.
	index map[string]map[objects.MAC]imap.UID
}

func NewStore(ctx context.Context, proto string, config map[string]string) (storage.Store, error) {
	s := &Store{
		location: config["location"],
		root:     defaultRoot,
		delim:    '/',
		index:    make(map[string]map[objects.MAC]imap.UID),
	}
	if err := s.connector.InitFromConfig(config); err != nil {
		return nil, err
	}
	if r := strings.Trim(config["root"], "/"); r != "" {
		s.root = r
	}
	return s, nil
}

func (s *Store) Origin() string        { return s.connector.Address }
func (s *Store) Type() string          { return "imap" }
func (s *Store) Root() string          { return s.location }
func (s *Store) Flags() location.Flags { return 0 }

func (s *Store) Mode(context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.withSession(func(sess *common.ImapSession) error {
		_, err := sess.Client.List("", "*", nil).Collect()
		return err
	})
}

// ---- session management ----------------------------------------------------

// withSession runs fn against the store's single long-lived connection,
// reconnecting if necessary. All store operations are serialized through the
// mutex; IMAP connection state (the selected mailbox) is per-connection so a
// single serialized session is the simplest correct model.
func (s *Store) withSession(fn func(*common.ImapSession) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withSessionLocked(fn)
}

func (s *Store) withSessionLocked(fn func(*common.ImapSession) error) error {
	if s.session == nil {
		sess, err := s.connector.Connect()
		if err != nil {
			return err
		}
		s.session = sess
		s.selected = ""
		// Discover the server hierarchy delimiter once.
		if list, err := sess.Client.List("", "", &imap.ListOptions{}).Collect(); err == nil {
			for _, l := range list {
				if l.Delim != 0 {
					s.delim = l.Delim
					break
				}
			}
		}
	}

	err := fn(s.session)
	if err != nil && isConnError(err) {
		// Drop the poisoned connection so the next call reconnects.
		_ = s.session.Logout()
		s.session = nil
		s.selected = ""
	}
	return err
}

func (s *Store) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		err := s.session.Logout()
		s.session = nil
		return err
	}
	return nil
}

// mailbox returns the fully-qualified mailbox name for a leaf, using the server
// delimiter (e.g. "Plakar.packfile" on Dovecot, "Plakar/packfile" elsewhere).
func (s *Store) mailbox(leaf string) string {
	return s.root + string(s.delim) + leaf
}

// selectMailbox selects mbox read-write on the current connection, tracking the
// currently-selected mailbox to avoid redundant SELECTs.
func (s *Store) selectMailbox(sess *common.ImapSession, mbox string) error {
	if s.selected == mbox {
		return nil
	}
	if _, err := sess.Client.Select(mbox, nil).Wait(); err != nil {
		s.selected = ""
		return err
	}
	s.selected = mbox
	return nil
}

// ---- helpers ----------------------------------------------------------------

func macToHex(mac objects.MAC) string { return hex.EncodeToString(mac[:]) }

func hexToMAC(s string) (objects.MAC, bool) {
	var mac objects.MAC
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != len(mac) {
		return mac, false
	}
	copy(mac[:], b)
	return mac, true
}

// buildMessage renders a blob into an RFC822 message carrying the MAC header
// and a base64-encoded body.
func buildMessage(name string, blob []byte) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s: %s\r\n", macHeader, name)
	buf.WriteString("Content-Transfer-Encoding: base64\r\n")
	buf.WriteString("Content-Type: application/octet-stream\r\n")
	buf.WriteString("\r\n")
	enc := base64.NewEncoder(base64.StdEncoding, &lineWrapper{w: &buf})
	enc.Write(blob)
	enc.Close()
	buf.WriteString("\r\n")
	return buf.Bytes()
}

// decodeBody extracts the blob from a message body produced by buildMessage:
// everything after the header/body separator, base64-decoded.
func decodeBody(raw []byte) ([]byte, error) {
	sep := bytes.Index(raw, []byte("\r\n\r\n"))
	var body []byte
	if sep >= 0 {
		body = raw[sep+4:]
	} else if sep = bytes.Index(raw, []byte("\n\n")); sep >= 0 {
		body = raw[sep+2:]
	} else {
		body = raw
	}
	// Strip whitespace/newlines that base64 decoding does not tolerate.
	clean := make([]byte, 0, len(body))
	for _, c := range body {
		if c == '\r' || c == '\n' || c == ' ' || c == '\t' {
			continue
		}
		clean = append(clean, c)
	}
	return base64.StdEncoding.DecodeString(string(clean))
}

func isConnError(err error) bool {
	if err == nil {
		return false
	}
	// An *imap.Error is a clean protocol-level NO/BAD response; everything else
	// (EOF, broken pipe, timeouts) means the connection is suspect.
	if _, ok := err.(*imap.Error); ok {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "use of closed")
}

// lineWrapper inserts CRLF every 76 base64 characters, keeping bodies within
// the line-length limits that strict SMTP/IMAP servers may enforce.
type lineWrapper struct {
	w   io.Writer
	col int
}

func (lw *lineWrapper) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		room := 76 - lw.col
		n := room
		if n > len(p) {
			n = len(p)
		}
		if _, err := lw.w.Write(p[:n]); err != nil {
			return written, err
		}
		written += n
		lw.col += n
		p = p[n:]
		if lw.col >= 76 {
			if _, err := lw.w.Write([]byte("\r\n")); err != nil {
				return written, err
			}
			lw.col = 0
		}
	}
	return written, nil
}
