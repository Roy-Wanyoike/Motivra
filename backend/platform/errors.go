package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// FieldError describes a single invalid request field.
type FieldError struct {
	Field string `json:"field"`
	Issue string `json:"issue"`
}

// Error is the RFC 7807-style application error returned to clients. It
// carries an HTTP status outside the JSON body and a stable machine code
// inside it.
type Error struct {
	Status      int          `json:"-"`
	Code        string       `json:"code"`
	Title       string       `json:"title"`
	Detail      string       `json:"detail,omitempty"`
	FieldErrors []FieldError `json:"errors,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	return e.Code
}

// ErrValidation reports an invalid request (HTTP 422).
func ErrValidation(detail string, fields ...FieldError) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Title: "The request is invalid", Detail: detail, FieldErrors: fields}
}

// ErrNotFound reports a missing resource (HTTP 404).
func ErrNotFound(detail string) *Error {
	return &Error{Status: http.StatusNotFound, Code: "not_found", Title: "Resource not found", Detail: detail}
}

// ErrUnauthorized reports missing or invalid authentication (HTTP 401).
func ErrUnauthorized(detail string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Title: "Authentication required", Detail: detail}
}

// ErrForbidden reports insufficient permissions (HTTP 403).
func ErrForbidden(detail string) *Error {
	return &Error{Status: http.StatusForbidden, Code: "forbidden", Title: "Permission denied", Detail: detail}
}

// ErrConflict reports a state conflict (HTTP 409).
func ErrConflict(detail string) *Error {
	return &Error{Status: http.StatusConflict, Code: "conflict", Title: "Conflict with current state", Detail: detail}
}

// ErrInternal reports an unexpected server failure (HTTP 500). Callers must
// log the underlying cause before discarding it; the response body never
// leaks internals.
func ErrInternal() *Error {
	return &Error{Status: http.StatusInternalServerError, Code: "internal_error", Title: "An unexpected error occurred"}
}

// AsError converts any error into an *Error, wrapping unknown errors as an
// internal error so handlers can pass failures through safely.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return ErrInternal()
}

// WriteError renders err as an application/problem+json response. Unknown
// errors are collapsed to a 500 without leaking internals.
func WriteError(w http.ResponseWriter, err error) {
	appErr := AsError(err)
	body, mErr := json.Marshal(appErr)
	if mErr != nil { // unreachable in practice; keep the handler total
		body = []byte(`{"code":"internal_error","title":"An unexpected error occurred"}`)
		appErr = ErrInternal()
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(appErr.Status)
	_, _ = w.Write(body)
}

// DecodeJSON decodes the request body into T with a byte-size cap. Parse
// failures map to ErrValidation so handlers can return them directly.
func DecodeJSON[T any](r *http.Request, maxBytes int64) (T, error) {
	var out T
	if r.Body == nil {
		return out, ErrValidation("request body is required")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBytes))
	if err := dec.Decode(&out); err != nil {
		return out, ErrValidation("request body is not valid JSON: " + err.Error())
	}
	return out, nil
}
