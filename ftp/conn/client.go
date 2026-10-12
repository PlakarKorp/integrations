package conn

import (
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/secsy/goftp"
)

// Client is a goftp client with the commands goftp does not wrap.  Those go
// over a control connection of their own, opened on first use and shared by
// concurrent callers.
type Client struct {
	*goftp.Client

	mu  sync.Mutex
	raw goftp.RawConn
}

// Chmod sets the permission, setuid, setgid and sticky bits of name with
// SITE CHMOD.  It is not part of RFC 959, and servers may lack or refuse it.
func (c *Client) Chmod(name string, perm fs.FileMode) error {
	mode := uint32(perm.Perm())
	if perm&fs.ModeSetuid != 0 {
		mode |= 0o4000
	}
	if perm&fs.ModeSetgid != 0 {
		mode |= 0o2000
	}
	if perm&fs.ModeSticky != 0 {
		mode |= 0o1000
	}

	code, msg, err := c.sendCommand("SITE CHMOD %04o %s", mode, name)
	if err != nil {
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	if code != 200 {
		return fmt.Errorf("chmod %s: %d %s", name, code, msg)
	}
	return nil
}

func (c *Client) sendCommand(f string, args ...any) (int, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.raw == nil {
		raw, err := c.OpenRawConn()
		if err != nil {
			return 0, "", err
		}
		c.raw = raw
	}

	code, msg, err := c.raw.SendCommand(f, args...)
	if err != nil {
		// Reconnect on the next call rather than fail every one after.
		_ = c.raw.Close()
		c.raw = nil
	}
	return code, msg, err
}

func (c *Client) Close() error {
	c.mu.Lock()
	var err error
	if c.raw != nil {
		err = c.raw.Close()
		c.raw = nil
	}
	c.mu.Unlock()
	return errors.Join(err, c.Client.Close())
}
