//go:build !linux

package dumpfs

import "errors"

var errUnsupported = errors.New("dumpfs: FUSE streaming is only supported on linux")

// Available reports whether Mount can work here: never on this platform.
func Available() error { return errUnsupported }

// FS is a mounted dumpfs filesystem.
type FS struct{}

// Mount always fails: FUSE streaming is linux only.
func Mount(dir string, emit EmitFunc, opts *Options) (*FS, error) { return nil, errUnsupported }

// Abort does nothing: an FS is never mounted on this platform.
func (f *FS) Abort(err error) {}

// Unmount does nothing: an FS is never mounted on this platform.
func (f *FS) Unmount() error { return nil }
