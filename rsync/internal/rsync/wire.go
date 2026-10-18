// Wire encoding and multiplexing, adapted from github.com/gokrazy/rsync
// (internal/rsyncwire). See LICENSE in this directory.

package rsync

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"strings"
)

// Multiplexed message tags (rsync/rsync.h, protocol < 30).
const (
	msgData  = 0
	msgError = 1
	msgInfo  = 2
	msgLog   = 3

	mplexBase = 7
)

// muxReader demultiplexes the server's output stream. Only data messages are
// returned to the caller. Error messages are collected rather than returned:
// a failure on one path does not abort the transfer.
type muxReader struct {
	r       io.Reader
	buf     []byte
	pending []byte
	errors  []string
}

func (m *muxReader) Read(p []byte) (int, error) {
	for len(m.pending) == 0 {
		var hdr [4]byte
		if _, err := io.ReadFull(m.r, hdr[:]); err != nil {
			return 0, err
		}
		header := binary.LittleEndian.Uint32(hdr[:])
		tag := int(header>>24) - mplexBase
		size := int(header & 0x00ffffff)
		if cap(m.buf) < size {
			m.buf = make([]byte, size)
		}
		buf := m.buf[:size]
		if _, err := io.ReadFull(m.r, buf); err != nil {
			return 0, err
		}
		switch tag {
		case msgData:
			m.pending = buf
		case msgError:
			m.errors = append(m.errors, strings.TrimSpace(string(buf)))
		case msgInfo, msgLog:
			log.Printf("rsync: %s", bytes.TrimSpace(buf))
		default:
			return 0, fmt.Errorf("unexpected multiplex tag %d", tag)
		}
	}
	n := copy(p, m.pending)
	m.pending = m.pending[n:]
	return n, nil
}

type wire struct {
	r io.Reader
	w io.Writer
}

func (c *wire) readByte() (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(c.r, b[:])
	return b[0], err
}

func (c *wire) readInt32() (int32, error) {
	var b [4]byte
	if _, err := io.ReadFull(c.r, b[:]); err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(b[:])), nil
}

// readInt64 reads a value sent as int32, or as -1 followed by an int64 when it
// does not fit.
func (c *wire) readInt64() (int64, error) {
	v, err := c.readInt32()
	if err != nil || v != -1 {
		return int64(v), err
	}
	var b [8]byte
	if _, err := io.ReadFull(c.r, b[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil
}

func (c *wire) readString(n int) (string, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(c.r, b)
	return string(b), err
}

func (c *wire) writeInt32(vs ...int32) error {
	b := make([]byte, 0, 4*len(vs))
	for _, v := range vs {
		b = binary.LittleEndian.AppendUint32(b, uint32(v))
	}
	_, err := c.w.Write(b)
	return err
}
