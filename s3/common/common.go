package common

import (
	"fmt"
	"strings"
)

func SplitVirtualHost(hostname, endpoint string) (bucket, host string, err error) {
	hostname = strings.TrimSuffix(hostname, ".") // foo.bar.com. -> foo.bar.com

	// sanitize the endpoint
	// we want to be able to compare it with the end of the hostname,
	// so we need to remove any protocol, trailing slash and leading dot.
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimSuffix(endpoint, "/")
	endpoint = strings.TrimPrefix(endpoint, ".")

	if endpoint == "" {
		return "", "", fmt.Errorf("empty endpoint")
	}

	bucket = strings.TrimSuffix(hostname, "."+endpoint) // foo.bar.com. -> foo.bar.com

	return bucket, endpoint, nil
}

// SplitPathStyle returns the bucket and the prefix of a path-style location.
// The location path names the bucket and root is the prefix inside it. A root
// that starts with that bucket, or a location without a bucket, keeps the
// older reading of root as /<bucket>/<prefix>.
func SplitPathStyle(locationPath, root string) (bucket, prefix string, err error) {
	bucket, prefix, _ = strings.Cut(strings.TrimPrefix(locationPath, "/"), "/")
	rootPath := strings.Trim(root, "/")
	if rootPath == "" {
		return bucket, prefix, nil
	}

	rootBucket, rootPrefix, _ := strings.Cut(strings.TrimPrefix(root, "/"), "/")
	if bucket == "" || rootBucket == bucket {
		return rootBucket, rootPrefix, nil
	}

	if locationPrefix := strings.Trim(prefix, "/"); locationPrefix != "" && locationPrefix != rootPath {
		return "", "", fmt.Errorf("location path %q conflicts with root %q", locationPath, root)
	}
	return bucket, strings.TrimPrefix(root, "/"), nil
}
