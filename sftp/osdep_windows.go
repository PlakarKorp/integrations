package sftp

import (
	"fmt"
	"os"
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

	return nil
}
