package errors

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	As  = errors.As
	Is  = errors.Is
	New = errors.New
)

func Wrap(err error, message string) error {
	if err != nil {
		err = fmt.Errorf("%s: %w", message, err)
	}
	return err
}

func Wrapf(err error, format string, args ...interface{}) error {
	if err != nil {
		err = fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
	}
	return err
}

// HTTPError associates an error with an HTTP status code. When returned to a
// gin handler through Response, the status code is used instead of the default
// 400 Bad Request. It implements Unwrap, so errors.Is / errors.As traverse
// into the underlying cause.
type HTTPError struct {
	status int
	err    error
}

func (e *HTTPError) Error() string { return e.err.Error() }
func (e *HTTPError) Unwrap() error { return e.err }
func (e *HTTPError) Status() int   { return e.status }

// NewHTTPError wraps err with the given HTTP status code.
func NewHTTPError(status int, err error) error {
	if err == nil {
		return nil
	}
	return &HTTPError{status: status, err: err}
}

// NewBadRequest returns an error that maps to HTTP 400 Bad Request.
func NewBadRequest(format string, args ...interface{}) error {
	return &HTTPError{status: http.StatusBadRequest, err: fmt.Errorf(format, args...)}
}

// NewUnauthorized returns an error that maps to HTTP 401 Unauthorized.
func NewUnauthorized(format string, args ...interface{}) error {
	return &HTTPError{status: http.StatusUnauthorized, err: fmt.Errorf(format, args...)}
}

// NewForbidden returns an error that maps to HTTP 403 Forbidden.
func NewForbidden(format string, args ...interface{}) error {
	return &HTTPError{status: http.StatusForbidden, err: fmt.Errorf(format, args...)}
}

// NewNotFound returns an error that maps to HTTP 404 Not Found.
func NewNotFound(format string, args ...interface{}) error {
	return &HTTPError{status: http.StatusNotFound, err: fmt.Errorf(format, args...)}
}

// NewConflict returns an error that maps to HTTP 409 Conflict.
func NewConflict(format string, args ...interface{}) error {
	return &HTTPError{status: http.StatusConflict, err: fmt.Errorf(format, args...)}
}

// NewInternal returns an error that maps to HTTP 500 Internal Server Error.
func NewInternal(format string, args ...interface{}) error {
	return &HTTPError{status: http.StatusInternalServerError, err: fmt.Errorf(format, args...)}
}

// The wrappers below attach an HTTP status to an existing error while
// preserving the error chain (so errors.Is / errors.As continue to traverse
// into the original cause). Prefer these over NewXxx("%s", err.Error()) when
// you already have an error value — the format-string constructors flatten
// the chain by design.

// Unauthorized wraps err with HTTP 401 Unauthorized.
func Unauthorized(err error) error { return NewHTTPError(http.StatusUnauthorized, err) }

// Forbidden wraps err with HTTP 403 Forbidden.
func Forbidden(err error) error { return NewHTTPError(http.StatusForbidden, err) }

// NotFound wraps err with HTTP 404 Not Found.
func NotFound(err error) error { return NewHTTPError(http.StatusNotFound, err) }

// Conflict wraps err with HTTP 409 Conflict.
func Conflict(err error) error { return NewHTTPError(http.StatusConflict, err) }

// Internal wraps err with HTTP 500 Internal Server Error.
func Internal(err error) error { return NewHTTPError(http.StatusInternalServerError, err) }
