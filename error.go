package musem

import (
	"errors"
	"fmt"
)

// Application error codes.
//
// These are transport-independent on purpose. An adapter reports what went
// wrong; the UI decides how it looks. Without this, either the adapter has to
// know about presentation or the UI has to string-match error text — and the
// second one breaks silently.
const (
	// EINTERNAL is an unclassified internal failure.
	EINTERNAL = "internal"
	// EINVALID is malformed or contradictory input.
	EINVALID = "invalid"
	// ENOTFOUND is a resource that does not exist.
	ENOTFOUND = "not_found"
	// EUNAVAILABLE is a source that could not be reached or is not installed.
	// The dashboard stays up and says so rather than failing.
	EUNAVAILABLE = "unavailable"
	// EUNPARSEABLE is a foreign payload whose shape musem does not recognise.
	EUNPARSEABLE = "unparseable"
)

// Staleness and unpriced models deliberately have no code here. Both were
// codes once, and neither is a failure: data served past its freshness window
// is reported by registry.Snapshot's Stale and Age, and usage from a model with
// no known rate by SessionCost.UnknownModels beside a Cost that knows it is
// unknown. Both are facts a snapshot carries about itself, and an error is the
// wrong shape for a fact — it can only be raised instead of the answer, not
// alongside it.

// Error is an application error carrying a code and a human-readable message.
type Error struct {
	Code    string
	Message string
	// Err is the wrapped cause, if any.
	Err error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("musem: %s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("musem: %s: %s", e.Code, e.Message)
}

// Unwrap exposes the wrapped cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an Error with a formatted message.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds an Error with a formatted message around an existing cause.
func Wrap(err error, code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// ErrorCode returns the application code for err, walking wrapped errors.
// Anything unrecognised is EINTERNAL, so callers always get a usable code.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return EINTERNAL
}

// ErrorMessage returns the human-readable message for err, walking wrapped
// errors. Unrecognised errors get a generic message rather than leaking
// internals into the UI.
func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "an internal error occurred"
}
