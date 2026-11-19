package xapi

import (
	"errors"
	"fmt"
	"strings"
)

// Well-known XAPI error codes.
const (
	CodeSessionInvalid              = "SESSION_INVALID"
	CodeSessionAuthenticationFailed = "SESSION_AUTHENTICATION_FAILED"
	CodeHostIsSlave                 = "HOST_IS_SLAVE"
	CodeHandleInvalid               = "HANDLE_INVALID"
	CodeOperationNotAllowed         = "OPERATION_NOT_ALLOWED"
	CodeVMBadPowerState             = "VM_BAD_POWER_STATE"
)

// Error is a failure reported by XAPI, as ["CODE", "param", ...].
type Error struct {
	Method string
	Code   string
	Params []string
}

func (e *Error) Error() string {
	if len(e.Params) == 0 {
		return fmt.Sprintf("xapi: %s: %s", e.Method, e.Code)
	}
	return fmt.Sprintf("xapi: %s: %s (%s)", e.Method, e.Code, strings.Join(e.Params, ", "))
}

// IsCode reports whether err is a XAPI *Error with the given code.
func IsCode(err error, code string) bool {
	xapiErr, ok := errors.AsType[*Error](err)
	return ok && xapiErr.Code == code
}
