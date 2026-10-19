package rsync

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"golang.org/x/crypto/md4" //nolint:staticcheck // the protocol mandates MD4
)

// chunkSize is the largest literal data token rsync sends.
const chunkSize = 32 << 10

// Send creates files in dir, a directory relative to the module root that
// must exist. File names are relative to dir, and rsync refuses one holding a
// "/" unless its parent is in files too. open returns the content of a regular
// file when the server asks for it.
func (c *Client) Send(ctx context.Context, dir string, files []File, open func(File) (io.Reader, error)) (err error) {
	files = slices.Clone(files)
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Name, b.Name) })
	flist, err := encodeFileList(files)
	if err != nil {
		return err
	}

	s, err := c.connect(ctx, false, sendOpts, path.Join(dir, ".")+"/")
	if err != nil {
		return err
	}
	defer s.close()
	defer func() {
		if err != nil {
			err = s.wrap(err)
		}
	}()

	// The server reads our output unmultiplexed: buffer the many small writes.
	w := bufio.NewWriterSize(s.conn, chunkSize+64)
	s.wire = &wire{r: s.mux, w: w}
	if _, err := w.Write(flist); err != nil {
		return err
	}

	// rsync/sender.c:send_files. The server asks for files by index, then
	// ends each of the two phases with -1; the first one is echoed.
	for phase := 0; ; {
		if err := w.Flush(); err != nil {
			return err
		}
		idx, err := s.readInt32()
		if err != nil {
			return err
		}
		if idx == -1 {
			if phase++; phase > 1 {
				break
			}
			if err := s.writeInt32(-1); err != nil {
				return err
			}
			continue
		}
		if idx < 0 || int(idx) >= len(files) || !files[idx].Mode.IsRegular() {
			return fmt.Errorf("protocol error: server asked for file index %d", idx)
		}
		if err := s.sendFile(idx, files[idx], open); err != nil {
			return err
		}
	}
	if err := s.writeInt32(-1); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// rsync/main.c:read_final_goodbye
	v, err := s.readInt32()
	if err != nil {
		return err
	}
	if v != -1 {
		return fmt.Errorf("protocol error: expected final goodbye, got %d", v)
	}
	// The receiver reports a file it could not write, and goes on.
	if len(s.mux.errors) > 0 {
		return errors.New("transfer incomplete")
	}
	return nil
}

// sendFile skips the checksums of the receiver's existing blocks, then sends
// the whole file as literal data followed by its checksum.
func (s *session) sendFile(idx int32, f File, open func(File) (io.Reader, error)) error {
	// count, block length, checksum length, remainder
	var head [4]int32
	for i := range head {
		v, err := s.readInt32()
		if err != nil {
			return err
		}
		head[i] = v
	}
	count, sumLen := head[0], head[2]
	if count < 0 || sumLen < 0 || sumLen > md4.Size {
		return fmt.Errorf("protocol error: invalid checksum header %v", head)
	}
	if _, err := io.CopyN(io.Discard, s.r, int64(count)*int64(4+sumLen)); err != nil {
		return err
	}

	rd, err := open(f)
	if err != nil {
		return err
	}

	if err := s.writeInt32(idx, head[0], head[1], head[2], head[3]); err != nil {
		return err
	}
	sum := s.fileChecksum()
	buf := make([]byte, chunkSize)
	for {
		n, err := io.ReadFull(rd, buf)
		if n > 0 {
			sum.Write(buf[:n])
			if err := s.writeInt32(int32(n)); err != nil {
				return err
			}
			if _, err := s.w.Write(buf[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	if err := s.writeInt32(0); err != nil {
		return err
	}
	_, err = s.w.Write(sum.Sum(nil))
	return err
}
