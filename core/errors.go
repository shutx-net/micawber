package core

import (
	"errors"
	"fmt"
)

// ErrNotFound reports that the requested content or asset does not exist.
var ErrNotFound = errors.New("micawber: not found")

// ErrExists reports that a create was attempted at a path that is already
// taken.
var ErrExists = errors.New("micawber: already exists")

// ErrConflict reports that a conditional operation was refused because the
// stored revision differs from the one the caller supplied.
var ErrConflict = errors.New("micawber: revision conflict")

// ErrInvalid reports that a caller-supplied value failed validation. Every
// *ValidationError unwraps to it.
var ErrInvalid = errors.New("micawber: invalid value")

// ErrUnsupported reports that a backend cannot perform the requested
// operation at all, as opposed to having refused this particular call.
var ErrUnsupported = errors.New("micawber: unsupported operation")

// ValidationError says which caller-supplied value was rejected and why. It
// unwraps to ErrInvalid, so callers can classify with
// errors.Is(err, ErrInvalid) and recover the detail with errors.As.
//
// Value echoes the offending input. That input is always content addressing —
// a path, a collection or an asset key — and never a credential, so the error
// is safe to log and to return to an API client.
type ValidationError struct {
	// Kind names the sort of value that failed, such as "content path".
	Kind string
	// Value is the offending input, echoed back for diagnosis.
	Value string
	// Reason states specifically why the value was rejected.
	Reason string
}

// Error returns a message naming the kind, the quoted value and the reason.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("micawber: invalid %s %q: %s", e.Kind, e.Value, e.Reason)
}

// Unwrap always returns ErrInvalid, making every validation failure matchable
// with a single sentinel however deeply it has been wrapped.
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// invalidf builds a *ValidationError whose Reason is formatted from format and
// args. It returns error rather than *ValidationError so that callers cannot
// accidentally hand back a non-nil error interface holding a nil pointer.
func invalidf(kind, value, format string, args ...any) error {
	return &ValidationError{
		Kind:   kind,
		Value:  value,
		Reason: fmt.Sprintf(format, args...),
	}
}
