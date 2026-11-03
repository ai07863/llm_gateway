package provider

import (
	"errors"
	"testing"
)

func TestIsRetryableClassifiesStatus(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{429, true},
		{500, true},
		{502, true},
		{503, true},
		{400, false},
		{401, false},
		{404, false},
	}
	for _, tc := range cases {
		e := NewStatusError("p", tc.status, "body")
		if got := IsRetryable(e); got != tc.want {
			t.Fatalf("status %d: want retryable=%v got %v", tc.status, tc.want, got)
		}
	}
}

func TestIsRetryableUnknownErrorIsRetryable(t *testing.T) {
	if !IsRetryable(errors.New("dial tcp 127.0.0.1: i/o timeout")) {
		t.Fatal("unknown errors should be treated as retryable")
	}
}

func TestIsRetryableNilIsFalse(t *testing.T) {
	if IsRetryable(nil) {
		t.Fatal("nil should not be retryable")
	}
}

func TestNetworkErrorIsRetryable(t *testing.T) {
	e := NewNetworkError("p", errors.New("connection refused"))
	if !IsRetryable(e) {
		t.Fatal("network errors must be retryable")
	}
}
