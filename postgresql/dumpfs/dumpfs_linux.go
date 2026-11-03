//go:build linux

package dumpfs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Available reports whether Mount can work here.
func Available() error {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		return fmt.Errorf("dumpfs: FUSE is not available: %w", err)
	}
	// Mount goes through fusermount3, falling back to fusermount as go-fuse does.
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			return fmt.Errorf("dumpfs: fusermount3 not found, install fuse3: %w", err)
		}
	}
	return nil
}

// FS is a mounted dumpfs filesystem.
type FS struct {
	server  *fuse.Server
	emit    EmitFunc
	opts    Options // with defaults filled in
	janitor *janitor

	// pieces holds the writers that gather pieces, so that a dump of many
	// small files does not allocate one per file.
	pieces sync.Pool

	mu sync.Mutex
	// open holds the files created and not yet finished: Abort walks it to
	// fail the pipe of each one, and Unmount waits for it to empty, since a
	// stream only ends in finish.
	open map[*handle]struct{}
	// finished is signalled, under mu, each time a file leaves open.
	finished *sync.Cond
	// aborted is the error Abort was called with. Once set, Create refuses
	// new files: a file created after an abort would get a pipe nobody reads,
	// and its writer would block forever.
	aborted error
}

// Mount mounts an empty dumpfs on dir, which must exist.
func Mount(dir string, emit EmitFunc, opts *Options) (*FS, error) {
	var o Options
	if opts != nil {
		o = *opts
	}
	if o.BlockSize == 0 {
		o.BlockSize = 1 << 20
	}
	if o.PieceSize == 0 {
		o.PieceSize = 1 << 20
	}
	if o.Name == "" {
		o.Name = "dumpfs"
	}
	f := &FS{
		emit: emit,
		opts: o,
		open: make(map[*handle]struct{}),
	}
	f.finished = sync.NewCond(&f.mu)
	f.pieces.New = func() any { return bufio.NewWriterSize(nil, o.PieceSize) }
	server, err := fs.Mount(dir, &root{fsys: f}, &fs.Options{
		// Owned by us, not root, when mounted through fusermount.
		UID: uint32(os.Getuid()),
		GID: uint32(os.Getgid()),
		// DirectMount is left off, so go-fuse mounts through the setuid
		// fusermount3 helper (Debian package fuse3), which hands back a
		// close-on-exec descriptor to /dev/fuse.
		//
		// With DirectMount, go-fuse would open /dev/fuse and call mount(2)
		// itself. That needs CAP_SYS_ADMIN, but no fusermount3: it works in
		// images without fuse3, under no-new-privileges where a setuid helper
		// cannot gain privileges, and without fusermount3's policy
		// (/etc/fuse.conf mount_max and user_allow_other, mount point
		// ownership checks). But it opens /dev/fuse without close-on-exec, so
		// every process we start, the writer included, inherits a copy. If we
		// die, that copy keeps the FUSE connection open, and the writer waits
		// forever for replies nobody sends.
		//
		// The fix belongs in go-fuse: as of v2.12.1, mountDirect in
		// fuse/mount_linux.go should call syscall.CloseOnExec on the
		// descriptor, as the fusermount path already does. DirectMount can be
		// turned back on once it does.
		MountOptions: fuse.MountOptions{
			FsName:   o.Name,
			Name:     "dumpfs",
			MaxWrite: fuse.MAX_KERNEL_WRITE,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("mount %s: %w", dir, err)
	}
	f.janitor, err = startJanitor(dir)
	if err != nil {
		return nil, errors.Join(err, server.Unmount())
	}
	f.server = server
	return f, nil
}

// Abort fails every file not released yet, whose readers and writers then
// get err, and refuses new files.
//
// It is what unblocks the writer when the reader stops: a writer then sits in
// a write that only a reader would complete. Closing the pipe makes that
// write fail.
func (f *FS) Abort(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborted = err
	for h := range f.open {
		h.pw.CloseWithError(err)
	}
}

// Unmount waits until every file is released, then unmounts the filesystem.
// If the filesystem cannot be unmounted, it is detached and dir removed.
func (f *FS) Unmount() error {
	// The kernel releases a file after close(2) returns: the writer may have
	// exited with releases still in flight.
	f.mu.Lock()
	for len(f.open) > 0 {
		f.finished.Wait()
	}
	f.mu.Unlock()
	err := f.server.Unmount()
	return errors.Join(err, f.janitor.stop(err == nil))
}

// janitor is a process that unmounts the filesystem and removes its directory
// once we are gone, so that a process killed mid-dump leaves no dead mount
// behind.
//
// Nothing in this process can do it: killed with SIGKILL, it runs nothing.
// The janitor reads a pipe whose other end only we hold, and the kernel
// closes it however we die. Unmount writes to it first, telling the janitor
// the filesystem is already unmounted.
type janitor struct {
	cmd  *exec.Cmd
	pipe *os.File
}

// janitorScript runs with the mount point as $0. read gets nothing when the
// pipe is closed without a word: we died, or Unmount failed.
const janitorScript = `read -r unmounted
[ -n "$unmounted" ] && exit 0
{ fusermount3 -u -z "$0" || fusermount -u -z "$0"; rmdir "$0"; } 2>/dev/null
exit 0`

func startJanitor(dir string) (*janitor, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("start janitor: %w", err)
	}
	defer r.Close()

	cmd := exec.Command("sh", "-c", janitorScript, dir)
	cmd.Stdin = r
	// Its own session: Ctrl-C sends SIGINT to the whole foreground process
	// group, and the janitor must outlive us.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		w.Close()
		return nil, fmt.Errorf("start janitor: %w", err)
	}
	return &janitor{cmd: cmd, pipe: w}, nil
}

// stop tells the janitor whether the filesystem was unmounted, then waits for
// it. Told nothing, it detaches the mount and removes the directory.
func (j *janitor) stop(unmounted bool) error {
	if unmounted {
		_, _ = fmt.Fprintln(j.pipe, "unmounted")
	}
	j.pipe.Close()
	if err := j.cmd.Wait(); err != nil {
		return fmt.Errorf("janitor: %w", err)
	}
	return nil
}

// root is the mount's only directory. Files are created directly in it (see
// the contract), so it only needs to create files: no subdirectories, no
// listing, no lookup of existing names.
type root struct {
	fs.Inode
	fsys *FS
}

var (
	_ fs.NodeCreater   = (*root)(nil)
	_ fs.NodeGetattrer = (*root)(nil)
	_ fs.NodeSetattrer = (*root)(nil)
)

func (r *root) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0o700
	return 0
}

// Setattr accepts a chmod of the directory, which a writer may apply to an
// output directory that already exists.
func (r *root) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	return r.Getattr(ctx, fh, out)
}

// Create is called when the writer creates a file. It only prepares the
// stream: the file reaches the reader later, on its first write or on release
// (see emit).
func (r *root) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	f := r.fsys
	pr, pw := io.Pipe()
	h := &handle{fsys: f, pw: pw}
	h.emit = sync.OnceValue(func() error { return f.emit(name, pr) })
	// A file that would block a reader: see the contract in the package doc.
	if f.opts.Hold != nil && f.opts.Hold(name, flags) {
		h.buf = new(bytes.Buffer)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.aborted != nil {
		return nil, nil, 0, syscall.EIO
	}
	f.open[h] = struct{}{}

	out.Mode = 0o600
	out.Blksize = f.opts.BlockSize
	node := r.NewInode(ctx, &file{blockSize: f.opts.BlockSize}, fs.StableAttr{Mode: syscall.S_IFREG})
	// FOPEN_DIRECT_IO bypasses the page cache: each write(2) reaches Write
	// as it happens, instead of being cached and written back later.
	return node, h, fuse.FOPEN_DIRECT_IO, 0
}

// file is a regular file. It has no Open method, so a file cannot be reopened
// once created: reading it back or reopening it to fsync both fail.
type file struct {
	fs.Inode
	blockSize uint32 // Options.BlockSize
}

var _ fs.NodeGetattrer = (*file)(nil)

func (n *file) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0o600
	out.Blksize = n.blockSize
	return 0
}

// handle is one file being written, from Create to finish.
type handle struct {
	fsys *FS

	mu sync.Mutex
	// pw is the write end of the pipe the reader reads the file from. A write
	// to it blocks until the reader has read it.
	pw *io.PipeWriter
	// out gathers writes into pieces of Options.PieceSize before they reach
	// pw. It is taken from fsys.pieces on the first write and given back in
	// finish.
	out *bufio.Writer
	// buf is non-nil only for a file Options.Hold selects. Writes go to buf
	// instead of pw, and finish writes buf to pw once the file is closed.
	buf *bytes.Buffer
	// emit hands the file to the EmitFunc, with the pipe's read end as its
	// content. It runs once, on the first Write or in finish, whichever comes
	// first; later calls return the first call's result without sending
	// anything. A failed handover thus fails every later call too, or a write
	// would block on a pipe nobody reads.
	emit func() error
	// off is how many bytes have gone through pw, which is where the next
	// write must start.
	off int64
}

var (
	_ fs.FileWriter   = (*handle)(nil)
	_ fs.FileReleaser = (*handle)(nil)
)

// Write is called for each write(2) on the file, split in pieces of at most
// MaxWrite. A writer using stdio writes each time its buffer fills or is
// flushed (see Options.BlockSize).
func (h *handle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// A held file: keep the content until the file is closed.
	if h.buf != nil {
		h.buf.Write(data)
		return uint32(len(data)), 0
	}
	// Hand the file over on the first write. On later writes this only
	// returns the first result.
	if err := h.emit(); err != nil {
		return 0, syscall.EIO
	}
	// Safeguard, not expected to trigger from a writer keeping to the
	// contract: the kernel sends one file's writes one at a time and in order,
	// since go-fuse does not enable async direct I/O. A write anywhere else
	// would mean a seek or a pwrite, which a stream cannot honour.
	if off != h.off {
		return 0, syscall.EIO
	}
	if h.out == nil {
		h.out = h.fsys.pieces.Get().(*bufio.Writer)
		h.out.Reset(h.pw)
	}
	// Blocks only when a full piece goes to pw, until the reader has read it.
	n, err := h.out.Write(data)
	h.off += int64(n)
	if err != nil {
		return uint32(n), syscall.EIO
	}
	return uint32(n), 0
}

// Release ends the file on its last close. Flush would come too early: it is
// sent on every close(2), including of a descriptor dup'ed by a shell or
// inherited by a fork.
func (h *handle) Release(ctx context.Context) syscall.Errno {
	// Emitting can block until the reader takes the file. The kernel runs at
	// most max_background RELEASE requests at once, so blocking here could
	// queue the RELEASE that ends the very stream being read.
	go h.finish()
	return 0
}

// finish emits the file if no write did, writes out any held content and
// ends the stream. It runs in its own goroutine, started by Release, because
// every step can block on the reader.
func (h *handle) finish() {
	f := h.fsys
	defer func() {
		f.mu.Lock()
		delete(f.open, h)
		f.finished.Broadcast()
		f.mu.Unlock()
	}()

	h.mu.Lock()
	defer h.mu.Unlock()
	// Hand the file over if no Write did: an empty file is never written to,
	// and a held file is only handed over now.
	if err := h.emit(); err != nil {
		h.pw.CloseWithError(err)
		return
	}
	// The last piece of a streamed file.
	if h.out != nil {
		err := h.out.Flush()
		h.out.Reset(nil)
		f.pieces.Put(h.out)
		h.out = nil
		if err != nil {
			return // aborted: the reader already has the error
		}
	}
	// A held file: its whole content goes out now.
	if h.buf != nil {
		if _, err := h.pw.Write(h.buf.Bytes()); err != nil {
			return // aborted: the reader already has the error
		}
	}
	// End of file for the reader.
	_ = h.pw.Close() // always nil; a no-op once aborted
}
