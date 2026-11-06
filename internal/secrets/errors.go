package secrets

import "errors"

// ErrRefRejected is returned when a secret name, path, or mount ref fails closed.
// It corresponds to contract code secret_ref_rejected.
var ErrRefRejected = errors.New("secret_ref_rejected")

// IsRefRejected reports whether err is a fail-closed secret ref rejection.
func IsRefRejected(err error) bool {
	return errors.Is(err, ErrRefRejected)
}
