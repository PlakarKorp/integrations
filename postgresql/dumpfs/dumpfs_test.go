//go:build linux

package dumpfs_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/postgresql/dumpfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collector reads every emitted file to completion in the background, the
// the way a consumer with many readers would.
type collector struct {
	read  chan struct{} // one per file read to the end
	mu    sync.Mutex
	files map[string][]byte
	errs  map[string]error
}

func newCollector() *collector {
	return &collector{read: make(chan struct{}, 16), files: map[string][]byte{}, errs: map[string]error{}}
}

func (c *collector) emit(name string, r io.ReadCloser) error {
	go func() {
		data, err := io.ReadAll(r)
		_ = r.Close()
		c.mu.Lock()
		c.files[name] = data
		c.errs[name] = err
		c.mu.Unlock()
		c.read <- struct{}{}
	}()
	return nil
}

// wait waits until n files have been read to the end.
func (c *collector) wait(n int) {
	for range n {
		<-c.read
	}
}

// holdAppend is the Hold rule of these tests: files opened with O_APPEND.
func holdAppend(name string, flags uint32) bool {
	return flags&syscall.O_APPEND != 0
}

// mount mounts a dumpfs that holds O_APPEND files.
func mount(t *testing.T, emit dumpfs.EmitFunc) (*dumpfs.FS, string) {
	t.Helper()
	return mountWith(t, emit, &dumpfs.Options{Hold: holdAppend})
}

func mountWith(t *testing.T, emit dumpfs.EmitFunc, opts *dumpfs.Options) (*dumpfs.FS, string) {
	t.Helper()
	if err := dumpfs.Available(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	fsys, err := dumpfs.Mount(dir, emit, opts)
	require.NoError(t, err)
	return fsys, dir
}

func TestStreamsFilesAsWritten(t *testing.T) {
	c := newCollector()
	fsys, dir := mount(t, c.emit)

	want := map[string][]byte{
		"index": []byte("small file"),
		"a":     bytes.Repeat([]byte("0123456789"), 512<<10), // spans many FUSE writes
		"b":     {},                                          // never written to
	}
	for name, content := range want {
		f, err := os.Create(filepath.Join(dir, name))
		require.NoError(t, err)
		_, err = f.Write(content)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	c.wait(len(want)) // each reader ends on its file's release
	require.NoError(t, fsys.Unmount())
	for name, content := range want {
		assert.NoError(t, c.errs[name], name)
		assert.Equal(t, content, c.files[name], name)
	}
}

func TestEmitsOnFirstWrite(t *testing.T) {
	c := newCollector()
	fsys, dir := mount(t, c.emit)

	f, err := os.Create(filepath.Join(dir, "a"))
	require.NoError(t, err)
	c.mu.Lock()
	assert.Empty(t, c.files, "emitted before the first write")
	c.mu.Unlock()

	_, err = f.Write([]byte("rows"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	c.wait(1)
	require.NoError(t, fsys.Unmount())
	assert.Equal(t, "rows", string(c.files["a"]))
}

func TestRejectsAccessAStreamCannotServe(t *testing.T) {
	c := newCollector()
	fsys, dir := mount(t, c.emit)

	name := filepath.Join(dir, "a")
	f, err := os.Create(name)
	require.NoError(t, err)
	_, err = f.WriteAt([]byte("x"), 10)
	assert.Error(t, err, "non-sequential write")
	require.NoError(t, f.Close())

	_, err = os.Open(name)
	assert.Error(t, err, "reopening a written file")

	c.wait(1)
	require.NoError(t, fsys.Unmount())
}

func TestAbortFailsUnfinishedFiles(t *testing.T) {
	cause := errors.New("writer failed")
	readers := make(chan io.ReadCloser, 1)
	fsys, dir := mount(t, func(name string, r io.ReadCloser) error {
		readers <- r
		return nil
	})

	f, err := os.Create(filepath.Join(dir, "a"))
	require.NoError(t, err)

	// Nobody reads, so this write blocks until Abort. It must be larger than
	// a piece: a smaller one would only fill the file's buffer.
	written := make(chan error, 1)
	go func() {
		_, err := f.Write(bytes.Repeat([]byte("x"), 2<<20))
		written <- err
	}()
	r := <-readers
	fsys.Abort(cause)

	assert.Error(t, <-written)
	_, err = io.ReadAll(r)
	assert.ErrorIs(t, err, cause)
	require.NoError(t, f.Close())

	_, err = os.Create(filepath.Join(dir, "b"))
	assert.Error(t, err, "file created after abort")
	require.NoError(t, fsys.Unmount())
}

// A held file is handed over only once closed, with all its content.
func TestAppendFileIsEmittedOnRelease(t *testing.T) {
	c := newCollector()
	fsys, dir := mount(t, c.emit)

	f, err := os.OpenFile(filepath.Join(dir, "held"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	require.NoError(t, err)
	for _, line := range []string{"part 1\n", "part 2\n"} {
		_, err := f.WriteString(line)
		require.NoError(t, err)
	}
	c.mu.Lock()
	assert.Empty(t, c.files, "emitted before release")
	c.mu.Unlock()
	require.NoError(t, f.Close())

	c.wait(1)
	require.NoError(t, fsys.Unmount())
	assert.Equal(t, "part 1\npart 2\n", string(c.files["held"]))
}

// sequential hands every emitted file to a single reader, one at a time, the
// as the scarcest consumer would.
func sequential(t *testing.T) dumpfs.EmitFunc {
	records := make(chan io.ReadCloser)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for r := range records {
			_, _ = io.Copy(io.Discard, r)
		}
	}()
	t.Cleanup(func() {
		close(records)
		<-done
	})
	return func(name string, r io.ReadCloser) error {
		records <- r
		return nil
	}
}

// A writer may keep a file open while it writes and closes others, as long as
// that file is held. A single reader must then get through the others.
func TestOpenHeldFileDoesNotHoldAReader(t *testing.T) {
	fsys, dir := mount(t, sequential(t))

	index, err := os.OpenFile(filepath.Join(dir, "held"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	require.NoError(t, err)
	for _, part := range []string{"part1", "part2"} {
		f, err := os.Create(filepath.Join(dir, part))
		require.NoError(t, err)
		_, err = f.Write(bytes.Repeat([]byte("x"), 1<<20))
		require.NoError(t, err)
		require.NoError(t, f.Close())
		_, err = index.WriteString(part + "\n")
		require.NoError(t, err)
	}
	require.NoError(t, index.Close())
	require.NoError(t, fsys.Unmount())
}

// The kernel runs at most max_background RELEASE requests at once (12 by
// default). Closing more files than that while a single reader is still on a
// stream must not queue that stream's own RELEASE behind them.
func TestManyReleasesDoNotBlockAStream(t *testing.T) {
	fsys, dir := mount(t, sequential(t))

	stream, err := os.Create(filepath.Join(dir, "a"))
	require.NoError(t, err)
	_, err = stream.Write([]byte("rows"))
	require.NoError(t, err)

	for i := range 20 {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("empty-%d", i))) // never written to
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	require.NoError(t, stream.Close())
	require.NoError(t, fsys.Unmount())
}

// A file that could not be handed over has no reader: every write to it must
// fail, not only the first, or the next one blocks forever.
func TestFailedEmitFailsEveryWrite(t *testing.T) {
	fsys, dir := mount(t, func(name string, r io.ReadCloser) error {
		return errors.New("records closed")
	})

	f, err := os.Create(filepath.Join(dir, "a"))
	require.NoError(t, err)
	_, err = f.Write([]byte("rows"))
	assert.Error(t, err, "first write")
	_, err = f.Write([]byte("rows"))
	assert.Error(t, err, "second write")
	require.NoError(t, f.Close())
	require.NoError(t, fsys.Unmount())
}

// A process we start must not hold the FUSE device: if we die, the kernel
// aborts the connection only once every descriptor to it is closed, and the
// writer would hang on it forever.
func TestChildrenDoNotInheritTheDevice(t *testing.T) {
	c := newCollector()
	fsys, _ := mount(t, c.emit)
	defer func() { require.NoError(t, fsys.Unmount()) }()

	out, err := exec.Command("sh", "-c", "ls -l /proc/self/fd/").CombinedOutput()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "/dev/fuse")
}

func TestAvailableNeedsFusermount(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip(err)
	}
	t.Setenv("PATH", t.TempDir())
	assert.ErrorContains(t, dumpfs.Available(), "fusermount3")
}

// mountedChild is set in the child process of TestKilledProcessLeavesNoMount
// to the directory it mounts before waiting to be killed.
const mountedChild = "DUMPFS_TEST_MOUNTED_CHILD"

func TestMain(m *testing.M) {
	if dir := os.Getenv(mountedChild); dir != "" {
		if _, err := dumpfs.Mount(dir, func(string, io.ReadCloser) error { return nil }, nil); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println("mounted")
		select {}
	}
	os.Exit(m.Run())
}

func isMounted(t *testing.T, dir string) bool {
	t.Helper()
	mounts, err := os.ReadFile("/proc/mounts")
	require.NoError(t, err)
	return strings.Contains(string(mounts), " "+dir+" ")
}

// Killed with SIGKILL, the process that mounted the filesystem never gets to
// unmount it. The mount and its directory must still go away.
func TestKilledProcessLeavesNoMount(t *testing.T) {
	if err := dumpfs.Available(); err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(t.TempDir(), "mnt")
	require.NoError(t, os.Mkdir(dir, 0o700))

	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), mountedChild+"="+dir)
	out, err := child.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.Start())
	line := make([]byte, len("mounted\n"))
	_, err = io.ReadFull(out, line)
	require.NoError(t, err)
	require.Equal(t, "mounted\n", string(line))
	require.True(t, isMounted(t, dir))

	require.NoError(t, child.Process.Kill())
	_ = child.Wait()

	// The janitor runs on its own once the child is gone: poll for it.
	assert.Eventually(t, func() bool {
		_, err := os.Stat(dir)
		return !isMounted(t, dir) && errors.Is(err, os.ErrNotExist)
	}, 10*time.Second, 50*time.Millisecond)
}

// A clean Unmount leaves the directory to its owner.
func TestUnmountKeepsTheDirectory(t *testing.T) {
	c := newCollector()
	fsys, dir := mount(t, c.emit)
	require.NoError(t, fsys.Unmount())
	assert.False(t, isMounted(t, dir))
	assert.DirExists(t, dir)
}

// Without Hold, every file is streamed, O_APPEND ones included.
func TestNoHoldStreamsEveryFile(t *testing.T) {
	emitted := make(chan io.ReadCloser, 1)
	fsys, dir := mountWith(t, func(name string, r io.ReadCloser) error {
		emitted <- r
		return nil
	}, nil)

	f, err := os.OpenFile(filepath.Join(dir, "log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("line\n")
	require.NoError(t, err)

	// Handed over on its first write, while the file is still open. A held
	// file would only be handed over on close.
	var r io.ReadCloser
	select {
	case r = <-emitted:
	case <-time.After(5 * time.Second):
		t.Fatal("not handed over on its first write")
	}

	require.NoError(t, f.Close())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "line\n", string(data))
	require.NoError(t, fsys.Unmount())
}

// A stdio writer sizes its buffer from the block size, and writes 8 KiB at a
// time at most: files must report a large block size, and the reader must
// get the writes gathered in pieces.
func TestBlockAndPieceSizes(t *testing.T) {
	tests := []struct {
		name  string
		opts  *dumpfs.Options
		block int
		piece int
	}{
		{"defaults", nil, 1 << 20, 1 << 20},
		{"set", &dumpfs.Options{BlockSize: 64 << 10, PieceSize: 64 << 10}, 64 << 10, 64 << 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reads := make(chan int, 1)
			fsys, dir := mountWith(t, func(name string, r io.ReadCloser) error {
				go func() {
					buf := make([]byte, 2*tt.piece)
					n, _ := io.ReadFull(r, buf[:1]) // wait for the first piece
					m, _ := r.Read(buf[n:])
					reads <- n + m
					_, _ = io.Copy(io.Discard, r)
				}()
				return nil
			}, tt.opts)

			f, err := os.Create(filepath.Join(dir, "a"))
			require.NoError(t, err)
			var st syscall.Stat_t
			require.NoError(t, syscall.Fstat(int(f.Fd()), &st))
			assert.EqualValues(t, tt.block, st.Blksize)

			piece := bytes.Repeat([]byte("x"), 8<<10)
			for range 2 * tt.piece / len(piece) { // two pieces in 8 KiB writes
				_, err := f.Write(piece)
				require.NoError(t, err)
			}
			require.NoError(t, f.Close())

			assert.Equal(t, tt.piece, <-reads, "first read")
			require.NoError(t, fsys.Unmount())
		})
	}
}
