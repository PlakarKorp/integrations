package common

import "net"

func splitHostPort(host string) (string, string, error) {
	return net.SplitHostPort(host)
}
