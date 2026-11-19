package xapi

import (
	"fmt"
	"strconv"
	"strings"
)

// Int64 is a XAPI integer field (virtual_size, memory_*, VCPUs_*). JSON-RPC
// 2.0 sends it as a number, but older encodings send it as a string, so both
// are accepted.
type Int64 int64

func (i *Int64) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		unquoted, err := strconv.Unquote(s)
		if err != nil {
			return fmt.Errorf("xapi: invalid integer %s", s)
		}
		s = unquoted
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("xapi: invalid integer %s", string(b))
	}
	*i = Int64(v)
	return nil
}
