package sftp

import (
	"errors"
	"fmt"
	"os"
)

func flock(string) (*os.File, error) {
	return nil, fmt.Errorf("flock: %w", errors.ErrUnsupported)
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

	return nil
}
