// File list encoding and decoding, adapted from github.com/gokrazy/rsync
// (internal/receiver/flist.go). See LICENSE in this directory.

package rsync

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"
)

// rsync/rsync.h, protocol < 28.
const (
	xmitSameMode    = 1 << 1
	xmitSameUID     = 1 << 3
	xmitSameGID     = 1 << 4
	xmitSameName    = 1 << 5
	xmitLongName    = 1 << 6
	xmitSameTime    = 1 << 7
	maxPathLen      = 4096
	modeTypeMask    = 0o170000
	modeRegular     = 0o100000
	modeDir         = 0o040000
	modeCharDevice  = 0o020000
	modeBlockDevice = 0o060000
	modeFIFO        = 0o010000
	modeSymlink     = 0o120000
	modeSocket      = 0o140000
)

// Name is relative to the requested path, "." being the path itself.
type File struct {
	Name       string
	Size       int64
	ModTime    time.Time
	Mode       fs.FileMode
	UID        uint32
	GID        uint32
	User       string
	Group      string
	LinkTarget string
}

// rawFile is an entry as sent on the wire. The previous one is kept to decode
// the XMIT_SAME_* flags.
type rawFile struct {
	name    string
	size    int64
	modTime int32
	mode    int32
	uid     int32
	gid     int32
	link    string
}

// receiveFileList reads a file list sent with the -l, -o, -g, -t and -p
// options and without -D, -H or -c.
func receiveFileList(c *wire) ([]File, error) {
	var files []File
	var last rawFile
	for {
		flags, err := c.readByte()
		if err != nil {
			return nil, err
		}
		if flags == 0 {
			break
		}
		f, err := receiveFileEntry(c, flags, &last)
		if err != nil {
			return nil, err
		}
		name := path.Clean(f.name)
		// A sane server only sends names below the requested directory.
		if name == ".." || strings.HasPrefix(name, "../") || path.IsAbs(name) {
			return nil, fmt.Errorf("server sent file name %q outside the requested directory", f.name)
		}
		files = append(files, File{
			Name:       name,
			Size:       f.size,
			ModTime:    time.Unix(int64(f.modTime), 0),
			Mode:       fileMode(f.mode),
			UID:        uint32(f.uid),
			GID:        uint32(f.gid),
			LinkTarget: f.link,
		})
		last = f
	}

	users, err := receiveIDList(c)
	if err != nil {
		return nil, err
	}
	groups, err := receiveIDList(c)
	if err != nil {
		return nil, err
	}
	// The I/O error flag: errors were already reported as messages.
	if _, err := c.readInt32(); err != nil {
		return nil, err
	}

	// Names are sent for every id but 0, which both sides take as root. No
	// names at all means the daemon uses numeric ids.
	named := len(users) > 0 || len(groups) > 0
	for i := range files {
		files[i].User = idName(users, files[i].UID, named)
		files[i].Group = idName(groups, files[i].GID, named)
	}
	return files, nil
}

// rsync/flist.c:receive_file_entry
func receiveFileEntry(c *wire, flags byte, last *rawFile) (rawFile, error) {
	var f rawFile

	var prefix int
	if flags&xmitSameName != 0 {
		l, err := c.readByte()
		if err != nil {
			return f, err
		}
		prefix = int(l)
	}
	var suffix int
	if flags&xmitLongName != 0 {
		l, err := c.readInt32()
		if err != nil {
			return f, err
		}
		suffix = int(l)
	} else {
		l, err := c.readByte()
		if err != nil {
			return f, err
		}
		suffix = int(l)
	}
	if prefix > len(last.name) || suffix < 0 || prefix+suffix >= maxPathLen {
		return f, fmt.Errorf("invalid file list entry name lengths %d+%d", prefix, suffix)
	}
	name, err := c.readString(suffix)
	if err != nil {
		return f, err
	}
	f.name = last.name[:prefix] + name

	if f.size, err = c.readInt64(); err != nil {
		return f, err
	}

	f.modTime = last.modTime
	if flags&xmitSameTime == 0 {
		if f.modTime, err = c.readInt32(); err != nil {
			return f, err
		}
	}
	f.mode = last.mode
	if flags&xmitSameMode == 0 {
		if f.mode, err = c.readInt32(); err != nil {
			return f, err
		}
	}
	f.uid = last.uid
	if flags&xmitSameUID == 0 {
		if f.uid, err = c.readInt32(); err != nil {
			return f, err
		}
	}
	f.gid = last.gid
	if flags&xmitSameGID == 0 {
		if f.gid, err = c.readInt32(); err != nil {
			return f, err
		}
	}
	if f.mode&modeTypeMask == modeSymlink {
		l, err := c.readInt32()
		if err != nil {
			return f, err
		}
		if l < 0 || l >= maxPathLen {
			return f, fmt.Errorf("invalid symlink target length %d", l)
		}
		if f.link, err = c.readString(int(l)); err != nil {
			return f, err
		}
	}
	return f, nil
}

// rsync/uidlist.c:recv_id_list. The server never sends id 0, which is also
// the list terminator.
func receiveIDList(c *wire) (map[int32]string, error) {
	names := make(map[int32]string)
	for {
		id, err := c.readInt32()
		if err != nil {
			return nil, err
		}
		if id == 0 {
			return names, nil
		}
		l, err := c.readByte()
		if err != nil {
			return nil, err
		}
		if names[id], err = c.readString(int(l)); err != nil {
			return nil, err
		}
	}
}

func idName(names map[int32]string, id uint32, named bool) string {
	if name, ok := names[int32(id)]; ok {
		return name
	}
	if id == 0 && named {
		return "root"
	}
	return strconv.FormatUint(uint64(id), 10)
}

func fileMode(mode int32) fs.FileMode {
	m := fs.FileMode(mode) & fs.ModePerm
	if mode&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if mode&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if mode&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	switch mode & modeTypeMask {
	case modeDir:
		m |= fs.ModeDir
	case modeSymlink:
		m |= fs.ModeSymlink
	case modeCharDevice:
		m |= fs.ModeDevice | fs.ModeCharDevice
	case modeBlockDevice:
		m |= fs.ModeDevice
	case modeFIFO:
		m |= fs.ModeNamedPipe
	case modeSocket:
		m |= fs.ModeSocket
	}
	return m
}

// encodeFileList encodes files as sent with the -l, -o, -g, -t and -p options,
// every field in full. The receiver refers to files by their index in the
// list sorted by name, so files must already be sorted.
func encodeFileList(files []File) ([]byte, error) {
	le := binary.LittleEndian
	var b []byte
	users := make(map[uint32]string)
	groups := make(map[uint32]string)
	for _, f := range files {
		if f.Name == "" || len(f.Name) >= maxPathLen || len(f.LinkTarget) >= maxPathLen {
			return nil, fmt.Errorf("%q: invalid file name or link target length", f.Name)
		}
		mode, err := unixMode(f.Mode)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		b = append(b, xmitLongName)
		b = le.AppendUint32(b, uint32(len(f.Name)))
		b = append(b, f.Name...)
		b = appendInt64(b, f.Size)
		var mtime int32
		if !f.ModTime.IsZero() {
			mtime = int32(f.ModTime.Unix())
		}
		b = le.AppendUint32(b, uint32(mtime))
		b = le.AppendUint32(b, uint32(mode))
		b = le.AppendUint32(b, f.UID)
		b = le.AppendUint32(b, f.GID)
		if f.Mode.Type() == fs.ModeSymlink {
			b = le.AppendUint32(b, uint32(len(f.LinkTarget)))
			b = append(b, f.LinkTarget...)
		}
		addIDName(users, f.UID, f.User)
		addIDName(groups, f.GID, f.Group)
	}
	b = append(b, 0) // end of list
	b = appendIDList(b, users)
	b = appendIDList(b, groups)
	b = le.AppendUint32(b, 0) // I/O errors
	return b, nil
}

// addIDName records the name to send for id. Id 0 is never sent, and names
// that are only the id carry nothing.
func addIDName(names map[uint32]string, id uint32, name string) {
	if id != 0 && name != "" && len(name) <= 255 && name != strconv.FormatUint(uint64(id), 10) {
		names[id] = name
	}
}

// rsync/uidlist.c:send_id_list
func appendIDList(b []byte, names map[uint32]string) []byte {
	for id, name := range names {
		b = binary.LittleEndian.AppendUint32(b, id)
		b = append(b, byte(len(name)))
		b = append(b, name...)
	}
	return binary.LittleEndian.AppendUint32(b, 0)
}

func unixMode(m fs.FileMode) (int32, error) {
	mode := int32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		mode |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		mode |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		mode |= 0o1000
	}
	switch m.Type() {
	case 0:
		mode |= modeRegular
	case fs.ModeDir:
		mode |= modeDir
	case fs.ModeSymlink:
		mode |= modeSymlink
	default:
		return 0, fmt.Errorf("unsupported file type %s", m.Type())
	}
	return mode, nil
}
