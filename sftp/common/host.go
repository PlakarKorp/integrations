package common

import (
	"fmt"
	"strings"
)

// checkHost rejects a hostname ssh would parse as an option.
//
// url.Parse is happy with "sftp://-oProxyCommand=.../path", which yields a
// Hostname() of "-oProxyCommand=..." -- appended to the ssh argv as a bare
// operand, that is an option, not a destination.
func checkHost(host string) error {
	if host == "" {
		return fmt.Errorf("missing hostname in endpoint")
	}
	if strings.HasPrefix(host, "-") {
		return fmt.Errorf("invalid hostname %q: may not start with a dash", host)
	}
	return nil
}
