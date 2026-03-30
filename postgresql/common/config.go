package common

import (
	"fmt"
	"strings"
)

// ParseBool parses a string as a boolean, accepting a wider set of values than
// strconv.ParseBool: "true", "t", "1", "yes", "y", "on" are true; "false",
// "f", "0", "no", "n", "off" are false.  Matching is case-insensitive.
func ParseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "t", "1", "yes", "y", "on":
		return true, nil
	case "false", "f", "0", "no", "n", "off":
		return false, nil
	}
	return false, fmt.Errorf("cannot parse %q as a boolean", s)
}
