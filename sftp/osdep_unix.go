//go:build !windows

package sftp

import (
	"fmt"
	"os"
	"syscall"
)

// checkPrivateDir verifies that dir is a directory owned by the
// current user.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}

	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}

	if fi.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%s is accessible to other users (mode %o)", dir, fi.Mode().Perm())
	}

	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uint64(st.Uid) != uint64(os.Getuid()) {
		return fmt.Errorf("%s is not owned by uid %d", dir, os.Getuid())
	}

	return nil
}
