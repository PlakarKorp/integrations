// Package dumpfs is a write-only FUSE filesystem that hands each file over as
// it is written. It lets a tool that writes a directory of files, several at
// once, have them read as streams without the files ever landing on local
// disk.
//
// # Files
//
// A file created at the root of the mount is handed to the EmitFunc on its
// first write, and read as a stream. Writes are gathered in pieces of
// Options.PieceSize, each blocking until the reader has read it, and the
// stream ends when the writer closes the file. A file never written to is
// handed over, empty, on close.
//
// Handing a file over on close blocks until the reader takes it, so it is done
// off the FUSE release handler: the kernel runs only a few releases at once,
// and blocked ones could hold back the release that ends the stream being
// read.
//
// # Contract
//
// A stream cannot go back or be read twice, so the writing tool must:
//
//   - write each file from start to end: a seek or a pwrite fails with EIO;
//   - never reopen a file once created, to read it or to fsync it;
//   - create files only at the root: there are no subdirectories;
//   - never wait on a file it has not closed while writing another. Each file
//     being read must make progress on its own, or a reader stays on a file
//     whose writer waits for a file nobody reads: with few readers, a
//     deadlock. A file that breaks this rule must be selected by
//     Options.Hold, which keeps it in memory and hands it over on close.
package dumpfs

import "io"

// EmitFunc receives each file created at the root of the mount, on its first
// write, or on release for a file never written to. r yields the content as
// it is written, in pieces of up to Options.PieceSize: writes block until r is
// read, so at most one piece per file is held. A file selected by
// Options.Hold is the exception: it is held in memory and emitted on release.
type EmitFunc func(name string, r io.ReadCloser) error

// Options configures a mount. A nil *Options, or a zero field, takes the
// default.
type Options struct {
	// Hold reports whether a file, given its name and open(2) flags, is held
	// in memory until closed instead of streamed: see the contract in the
	// package doc. nil holds no file.
	Hold func(name string, flags uint32) bool

	// BlockSize is the block size files report (st_blksize). Tools writing
	// through stdio size their buffer from it, up to glibc's BUFSIZ (8 KiB):
	// left at the kernel's 4 KiB, every 4 KiB would cost a FUSE round trip.
	// Default 1 MiB.
	BlockSize uint32

	// PieceSize is how much of a file is gathered before it reaches the
	// reader. Writes passed on as they come, as small as a stdio buffer, would
	// each cost the reader a read and a goroutine handoff. Default 1 MiB.
	PieceSize int

	// Name is the filesystem name, the source column of /proc/mounts.
	// Default "dumpfs".
	Name string
}
