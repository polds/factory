package invocation

import "errors"

// Reject is an admission failure that writes nothing to Postgres.
type Reject struct {
	Code   string
	Status int
}

func (r Reject) Error() string { return r.Code }

// ErrConflict is returned when the occurrence key exists with a different digest.
var ErrConflict = errors.New("invocation_conflict")

const (
	CodeInputInvalid       = "input_invalid"
	CodePrincipalRejected  = "principal_rejected"
	CodeProfileRejected    = "profile_rejected"
	CodeRepositoryRejected = "repository_rejected"
	CodeGrantRejected      = "grant_rejected"
)
