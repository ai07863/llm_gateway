package provider

import (
	"errors"
	"fmt"
)

type Error struct {
	Provider   string
	StatusCode int
	Retryable  bool
	Err        error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "provider error"
}

func (e *Error) Unwrap() error { return e.Err }

func NewStatusError(providerName string, status int, body string) *Error {
	return &Error{
		Provider:   providerName,
		StatusCode: status,
		Retryable:  status == 429 || status >= 500,
		Err:        fmt.Errorf("provider %s status %d: %s", providerName, status, body),
	}
}

func NewNetworkError(providerName string, err error) *Error {
	return &Error{Provider: providerName, Retryable: true, Err: err}
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	return true
}
