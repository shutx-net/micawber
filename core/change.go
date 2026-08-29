package core

import (
	"fmt"
	"strings"
	"time"
)

// Author is who a change is attributed to.
//
// Name and Email are the user's, not the backend's: they belong to the
// operation, not to provider configuration, which is why they travel with the
// call rather than hiding in a constructor argument or in a context.
type Author struct {
	// Name is the author's display name. It is required.
	Name string
	// Email is the author's e-mail address. It is optional; when present it
	// must contain an "@".
	Email string
}

// Validate reports whether a is usable as authorship.
//
// The name must be non-empty and not merely whitespace, and neither field may
// contain a control character. That last rule is injection defence: an adapter
// may put these strings on a command line or into a wire protocol, and a
// newline in a name is how a value becomes a second field.
//
// The returned error unwraps to ErrInvalid.
func (a Author) Validate() error {
	const kind = "author"

	if strings.TrimSpace(a.Name) == "" {
		return invalidf(kind, a.Name, "has an empty name")
	}
	if r, ok := controlRune(a.Name, ""); ok {
		return invalidf(kind, a.Name, "name contains the control character %q", r)
	}
	if r, ok := controlRune(a.Email, ""); ok {
		return invalidf(kind, a.Email, "e-mail address contains the control character %q", r)
	}
	if a.Email != "" && !strings.Contains(a.Email, "@") {
		return invalidf(kind, a.Email, `e-mail address has no "@"`)
	}
	return nil
}

// String returns "Name <email>", or just the name when Email is empty.
func (a Author) String() string {
	if a.Email == "" {
		return a.Name
	}
	return a.Name + " <" + a.Email + ">"
}

// Change is the authorship a mutating operation records: why it happened, who
// did it and when.
//
// Every mutation in a Git-native CMS becomes a commit, so this travels with
// Put and Delete. Message, author and time are generic version-control
// concepts, not Git APIs, so no provider knowledge enters the core: a backend
// with no notion of authorship simply ignores the Change.
type Change struct {
	// Message describes the change. It is required and may span lines.
	Message string
	// Author is who the change is attributed to.
	Author Author
	// Time is when the change was made. The zero time means the backend
	// supplies its own clock.
	Time time.Time
}

// Validate reports whether ch is usable. The message must be non-empty and not
// merely whitespace, and may contain no control character other than a tab or
// a line break, since messages are legitimately multi-line. The author must
// pass Author.Validate.
//
// The returned error unwraps to ErrInvalid.
func (ch Change) Validate() error {
	const kind = "change"

	if strings.TrimSpace(ch.Message) == "" {
		return invalidf(kind, ch.Message, "has an empty message")
	}
	if r, ok := controlRune(ch.Message, "\t\n\r"); ok {
		return invalidf(kind, ch.Message, "message contains the control character %q", r)
	}
	if err := ch.Author.Validate(); err != nil {
		return fmt.Errorf("change: %w", err)
	}
	return nil
}

// controlRune returns the first control character in s that is not listed in
// allow, and whether there was one.
func controlRune(s, allow string) (rune, bool) {
	for _, r := range s {
		if (r < 0x20 || r == 0x7f) && !strings.ContainsRune(allow, r) {
			return r, true
		}
	}
	return 0, false
}
