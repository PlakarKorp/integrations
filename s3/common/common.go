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
