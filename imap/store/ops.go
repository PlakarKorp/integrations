package store

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/PlakarKorp/integrations/imap/common"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/emersion/go-imap/v2"
)

// Create initializes the repository: it creates the root mailbox and one
// mailbox per resource type, then stores the configuration blob. It fails if a
// configuration already exists, mirroring the "create must be empty" contract
// of the other stores.
func (s *Store) Create(ctx context.Context, configuration []byte) error {
	return s.withSession(func(sess *common.ImapSession) error {
		// Create the root and all resource mailboxes.
		if err := sess.Create(s.root, true); err != nil {
			return fmt.Errorf("create root mailbox %q: %w", s.root, err)
		}
		for _, leaf := range []string{"packfile", "state", "lock", "eccpackfile", "eccstate", configName} {
			mbox := s.mailbox(leaf)
			if err := sess.Create(mbox, true); err != nil {
				return fmt.Errorf("create mailbox %q: %w", mbox, err)
			}
		}

		cfgMbox := s.mailbox(configName)
		// findUID selects the mailbox as a side effect.
		if uid, err := s.findUID(sess, cfgMbox, configName); err == nil && uid != 0 {
			return fmt.Errorf("repository already exists at %q", s.location)
		}

		return s.appendBlob(sess, cfgMbox, configName, configuration)
	})
}

// Open returns the stored configuration blob.
func (s *Store) Open(ctx context.Context) ([]byte, error) {
	var out []byte
	err := s.withSession(func(sess *common.ImapSession) error {
		cfgMbox := s.mailbox(configName)
		uid, err := s.findUID(sess, cfgMbox, configName)
		if err != nil {
			return err
		}
		if uid == 0 {
			return fmt.Errorf("repository configuration not found at %q", s.location)
		}
		blob, err := s.fetchBlob(sess, uid, nil)
		if err != nil {
			return err
		}
		out = blob
		return nil
	})
	return out, err
}

func (s *Store) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	leaf, ok := resourceMailbox(res)
	if !ok {
		return nil, nil
	}
	var macs []objects.MAC
	err := s.withSession(func(sess *common.ImapSession) error {
		mbox := s.mailbox(leaf)
		idx, err := s.refreshIndex(sess, mbox)
		if err != nil {
			return err
		}
		macs = make([]objects.MAC, 0, len(idx))
		for mac := range idx {
			macs = append(macs, mac)
		}
		return nil
	})
	return macs, err
}

func (s *Store) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	leaf, ok := resourceMailbox(res)
	if !ok {
		return -1, fmt.Errorf("unsupported resource %v", res)
	}
	blob, err := io.ReadAll(rd)
	if err != nil {
		return -1, err
	}
	err = s.withSession(func(sess *common.ImapSession) error {
		mbox := s.mailbox(leaf)
		if err := sess.Create(mbox, true); err != nil {
			return err
		}
		if err := s.appendBlob(sess, mbox, macToHex(mac), blob); err != nil {
			return err
		}
		// The new message's UID is unknown until the next refresh; drop any
		// cached entry so a subsequent Get re-lists and learns it.
		s.dropFromIndex(mbox, mac)
		return nil
	})
	if err != nil {
		return -1, err
	}
	return int64(len(blob)), nil
}

func (s *Store) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	leaf, ok := resourceMailbox(res)
	if !ok {
		return nil, fmt.Errorf("unsupported resource %v", res)
	}
	var out []byte
	err := s.withSession(func(sess *common.ImapSession) error {
		mbox := s.mailbox(leaf)
		uid, err := s.lookupUID(sess, mbox, macToHex(mac))
		if err != nil {
			return err
		}
		if uid == 0 {
			return fmt.Errorf("blob %s not found", macToHex(mac))
		}
		blob, err := s.fetchBlob(sess, uid, rg)
		if err != nil {
			return err
		}
		out = blob
		return nil
	})
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(out)), nil
}

func (s *Store) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	leaf, ok := resourceMailbox(res)
	if !ok {
		return fmt.Errorf("unsupported resource %v", res)
	}
	return s.withSession(func(sess *common.ImapSession) error {
		mbox := s.mailbox(leaf)
		uid, err := s.lookupUID(sess, mbox, macToHex(mac))
		if err != nil {
			return err
		}
		if uid == 0 {
			return nil // already gone
		}
		if err := s.selectMailbox(sess, mbox); err != nil {
			return err
		}
		var set imap.UIDSet
		set.AddNum(uid)
		store := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}
		if err := sess.Client.Store(set, store, nil).Close(); err != nil {
			return err
		}
		if err := sess.Client.Expunge().Close(); err != nil {
			return err
		}
		s.dropFromIndex(mbox, mac)
		return nil
	})
}

func (s *Store) Size(ctx context.Context) (int64, error) {
	// Computing an exact byte size would require fetching every message; report
	// 0 as the mock backend does. (Callers are warned this can be costly.)
	return 0, nil
}

// ---- blob primitives --------------------------------------------------------

// appendBlob APPENDs a blob as a message into mbox.
func (s *Store) appendBlob(sess *common.ImapSession, mbox, name string, blob []byte) error {
	msg := buildMessage(name, blob)
	cmd := sess.Client.Append(mbox, int64(len(msg)), nil)
	if _, err := cmd.Write(msg); err != nil {
		_ = cmd.Close()
		return err
	}
	if err := cmd.Close(); err != nil {
		return err
	}
	_, err := cmd.Wait()
	return err
}

// fetchBlob fetches and decodes the blob carried by a message UID. If rg is
// non-nil, only the requested byte range of the decoded blob is returned.
func (s *Store) fetchBlob(sess *common.ImapSession, uid imap.UID, rg *storage.Range) ([]byte, error) {
	var set imap.UIDSet
	set.AddNum(uid)
	fopts := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	}
	msgs, err := sess.Client.Fetch(set, fopts).Collect()
	if err != nil {
		return nil, err
	}
	if len(msgs) != 1 || len(msgs[0].BodySection) != 1 {
		return nil, fmt.Errorf("unexpected fetch response for uid %d", uid)
	}
	blob, err := decodeBody(msgs[0].BodySection[0].Bytes)
	if err != nil {
		return nil, fmt.Errorf("decode blob: %w", err)
	}
	if rg != nil {
		start := int(rg.Offset)
		end := start + int(rg.Length)
		if start > len(blob) {
			start = len(blob)
		}
		if end > len(blob) {
			end = len(blob)
		}
		blob = blob[start:end]
	}
	return blob, nil
}

// ---- MAC -> UID index -------------------------------------------------------

// refreshIndex selects mbox and rebuilds the MAC->UID map by reading the MAC
// header of every message.
func (s *Store) refreshIndex(sess *common.ImapSession, mbox string) (map[objects.MAC]imap.UID, error) {
	sel, err := sess.Client.Select(mbox, nil).Wait()
	if err != nil {
		return nil, err
	}
	s.selected = mbox

	idx := make(map[objects.MAC]imap.UID)
	if sel.NumMessages == 0 {
		s.setIndex(mbox, idx)
		return idx, nil
	}

	var seq imap.SeqSet
	seq.AddRange(1, sel.NumMessages)
	fopts := &imap.FetchOptions{
		UID: true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{macHeader}, Peek: true},
		},
	}
	msgs, err := sess.Client.Fetch(seq, fopts).Collect()
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if m.UID == 0 || len(m.BodySection) == 0 {
			continue
		}
		name := parseMACHeader(m.BodySection[0].Bytes)
		if mac, ok := hexToMAC(name); ok {
			idx[mac] = m.UID
		}
	}
	s.setIndex(mbox, idx)
	return idx, nil
}

// lookupUID returns the UID for a blob name, consulting (and refreshing) the
// per-mailbox index. The caller holds s.mu (via withSession), so the index
// maps are accessed without further locking.
func (s *Store) lookupUID(sess *common.ImapSession, mbox, name string) (imap.UID, error) {
	if mac, ok := hexToMAC(name); ok {
		if m := s.index[mbox]; m != nil {
			if uid, found := m[mac]; found {
				return uid, nil
			}
		}
	}
	idx, err := s.refreshIndex(sess, mbox)
	if err != nil {
		return 0, err
	}
	if mac, ok := hexToMAC(name); ok {
		return idx[mac], nil
	}
	return 0, nil
}

// findUID locates a non-MAC named blob (the config) by scanning MAC headers for
// an exact string match.
func (s *Store) findUID(sess *common.ImapSession, mbox, name string) (imap.UID, error) {
	sel, err := sess.Client.Select(mbox, nil).Wait()
	if err != nil {
		return 0, err
	}
	s.selected = mbox
	if sel.NumMessages == 0 {
		return 0, nil
	}
	var seq imap.SeqSet
	seq.AddRange(1, sel.NumMessages)
	fopts := &imap.FetchOptions{
		UID: true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{macHeader}, Peek: true},
		},
	}
	msgs, err := sess.Client.Fetch(seq, fopts).Collect()
	if err != nil {
		return 0, err
	}
	for _, m := range msgs {
		if m.UID == 0 || len(m.BodySection) == 0 {
			continue
		}
		if parseMACHeader(m.BodySection[0].Bytes) == name {
			return m.UID, nil
		}
	}
	return 0, nil
}

// setIndex and dropFromIndex are always called while s.mu is held (via
// withSession), so they touch the index maps directly.
func (s *Store) setIndex(mbox string, idx map[objects.MAC]imap.UID) {
	s.index[mbox] = idx
}

func (s *Store) dropFromIndex(mbox string, mac objects.MAC) {
	if m := s.index[mbox]; m != nil {
		delete(m, mac)
	}
}

// parseMACHeader extracts the X-Plakar-Mac header value from a header blob.
func parseMACHeader(raw []byte) string {
	prefix := []byte(macHeader + ":")
	for _, line := range splitLines(raw) {
		if len(line) >= len(prefix) && equalFoldASCII(line[:len(prefix)], prefix) {
			return string(bytes.TrimSpace(line[len(prefix):]))
		}
	}
	return ""
}

func splitLines(raw []byte) [][]byte {
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	return bytes.Split(raw, []byte("\n"))
}

func equalFoldASCII(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
