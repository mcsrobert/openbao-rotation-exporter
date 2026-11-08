package openbao

import "errors"

// Failure reasons, used as metric label values.
const (
	ReasonCredentials        = "credentials"
	ReasonForbidden          = "forbidden"
	ReasonHTTPError          = "http_error"
	ReasonInvalidResponse    = "invalid_response"
	ReasonNotFound           = "not_found"
	ReasonTimeout            = "timeout"
	ReasonUnavailable        = "unavailable"
	ReasonUnknown            = "unknown"
	ReasonVersionUnavailable = "version_unavailable"
)

// Error is a failure with a bounded reason.
type Error struct {
	Reason string
	Err    error
}

func (e *Error) Error() string {
	return e.Reason + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error {
	return e.Err
}

// ReasonOf returns the reason carried by err, or ReasonUnknown.
func ReasonOf(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Reason
	}
	return ReasonUnknown
}
