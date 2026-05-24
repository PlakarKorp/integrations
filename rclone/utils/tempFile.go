package utils

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
)

// AutoremoveTmpFile is a wrapper around an os.File that removes the file when it's closed.
type AutoremoveTmpFile struct {
	*os.File
}

func (file *AutoremoveTmpFile) Close() error {
	defer os.Remove(file.Name())
	return file.File.Close()
}

func nextRandom() string {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}

func CreateTempPath(originalPath string) (path string, err error) {
	tmpPath := os.TempDir() + "/" + originalPath
	prefix, suffix := "", ""
	if i := strings.LastIndex(tmpPath, "*"); i >= 0 {
		prefix, suffix = tmpPath[:i], tmpPath[i+1:]
	} else {
		prefix = tmpPath
	}

	for i := 0; i < 10000; i++ {
		name := prefix + nextRandom() + suffix
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return name, nil
		}
	}
	return "", fmt.Errorf("failed to find a folder to create the temporary file")
}
