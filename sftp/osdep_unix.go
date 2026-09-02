//go:build !windows

package sftp

import (
	"fmt"
	"os"
	"syscall"
)

func flock(p string) (*os.File, error) {
	fp, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", p, err)
	}

	for {
		err = syscall.Flock(int(fp.Fd()), syscall.LOCK_EX)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			fp.Close()
			return nil, err
		}

		break
	}

	return fp, nil
}

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
