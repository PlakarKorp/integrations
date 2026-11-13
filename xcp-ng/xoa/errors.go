package xoa

import "fmt"

// Error is a non-2xx response from Xen Orchestra.
type Error struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("xoa: %s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("xoa: %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}
