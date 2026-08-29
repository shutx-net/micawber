package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestSentinelErrorsAreDistinct(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrNotFound", ErrNotFound},
		{"ErrExists", ErrExists},
		{"ErrConflict", ErrConflict},
		{"ErrInvalid", ErrInvalid},
		{"ErrUnsupported", ErrUnsupported},
	}

	for _, s := range sentinels {
		if s.err == nil {
			t.Errorf("%s is nil", s.name)
			continue
		}
		if !strings.HasPrefix(s.err.Error(), "micawber: ") {
			t.Errorf("%s.Error() = %q, want a %q prefix", s.name, s.err.Error(), "micawber: ")
		}
	}

	for _, a := range sentinels {
		for _, b := range sentinels {
			got := errors.Is(a.err, b.err)
			want := a.name == b.name
			if got != want {
				t.Errorf("errors.Is(%s, %s) = %t, want %t", a.name, b.name, got, want)
			}
		}
	}
}

func TestValidationErrorUnwrapsToErrInvalid(t *testing.T) {
	verr := &ValidationError{
		Kind:   "content path",
		Value:  "../x.md",
		Reason: `contains a ".." segment`,
	}
	wrapped := fmt.Errorf("get %q: %w", "../x.md", verr)

	if !errors.Is(wrapped, ErrInvalid) {
		t.Errorf("errors.Is(wrapped, ErrInvalid) = false, want true")
	}
	if errors.Is(wrapped, ErrNotFound) {
		t.Errorf("errors.Is(wrapped, ErrNotFound) = true, want false")
	}

	var target *ValidationError
	if !errors.As(wrapped, &target) {
		t.Fatalf("errors.As(wrapped, &target) = false, want true")
	}
	if target.Kind != verr.Kind {
		t.Errorf("Kind = %q, want %q", target.Kind, verr.Kind)
	}
	if target.Value != verr.Value {
		t.Errorf("Value = %q, want %q", target.Value, verr.Value)
	}
	if target.Reason != verr.Reason {
		t.Errorf("Reason = %q, want %q", target.Reason, verr.Reason)
	}
}

func TestValidationErrorMessageNamesKindValueAndReason(t *testing.T) {
	verr := &ValidationError{
		Kind:   "asset key",
		Value:  "a//b.png",
		Reason: "contains an empty segment",
	}
	msg := verr.Error()

	for _, want := range []string{"asset key", strconv.Quote("a//b.png"), "contains an empty segment"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want it to contain %q", msg, want)
		}
	}
}
