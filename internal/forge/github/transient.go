package github

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
)

// StatusError is a GitHub HTTP response other than 403 and 404.
// Those two are PermissionError.
type StatusError struct {
	Op         string
	Method     string
	Path       string
	StatusCode int
	Detail     string
}

func (e *StatusError) Error() string {
	detail := e.Detail
	if detail == "" {
		detail = "request rejected"
	}
	return fmt.Sprintf("github: %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, detail)
}

// Transient reports a token-fetch failure that is worth retrying.
// Timeouts, connection reset and refusal, DNS errors, and HTTP 5xx are
// transient. context.Canceled, HTTP 401 and 403, and a bad private key are not.
func Transient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var status *StatusError
	if errors.As(err, &status) {
		return status.StatusCode >= 500 && status.StatusCode <= 599
	}
	var perm *PermissionError
	if errors.As(err, &perm) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.ECONNABORTED, syscall.ETIMEDOUT, syscall.EHOSTUNREACH, syscall.ENETUNREACH:
			return true
		default:
			return false
		}
	}
	return false
}
