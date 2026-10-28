package assess

import (
	"errors"
	"fmt"
)

// Class says what to do about a judgement that failed.
type Class string

const (
	// Permanent: the same request fails the same way again (a page too
	// long, a request the API refuses, a key or billing problem). Retrying
	// costs money for nothing; it goes straight to the dead-letter queue.
	Permanent Class = "permanent"
	// Transient: the service was busy or down, or the batch ran out of
	// time. The same request usually works later.
	Transient Class = "transient"
	// Unknown: anything else, such as a reply without the tool call.
	// Treated like a transient error, because one more try is cheap and
	// often works, but counted apart so a pattern shows.
	Unknown Class = "unknown"
)

// ClassError is a failed judgement with its class.
type ClassError struct {
	Class Class
	Err   error
}

func (e *ClassError) Error() string { return e.Err.Error() }
func (e *ClassError) Unwrap() error { return e.Err }

// ClassOf returns the class an error carries; a plain error is Unknown.
func ClassOf(err error) Class {
	var ce *ClassError
	if errors.As(err, &ce) {
		return ce.Class
	}
	return Unknown
}

// errorClasses maps the API's error types to a class.
// https://docs.anthropic.com/en/api/errors
var errorClasses = map[string]Class{
	"invalid_request_error": Permanent,
	"request_too_large":     Permanent,
	"authentication_error":  Permanent,
	"permission_error":      Permanent,
	"not_found_error":       Permanent,
	"billing_error":         Permanent,
	"rate_limit_error":      Transient,
	"api_error":             Transient,
	"overloaded_error":      Transient,
	"timeout_error":         Transient,
}

// apiError classes an error the API reported with its type and message.
func apiError(errType, message string) error {
	class, ok := errorClasses[errType]
	if !ok {
		class = Unknown
	}
	return &ClassError{Class: class, Err: fmt.Errorf("anthropic: %s: %s", errType, message)}
}
