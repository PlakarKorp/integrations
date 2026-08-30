//go:build windows

package common

import (
	"fmt"
	"os"
)

// checkPrivateDir verifies that dir exists and is a directory.  Windows has no
// mode bits to check here; os.UserCacheDir already resolves to a per-user
// location under the user's profile.
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
