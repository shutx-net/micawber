package localfs

import (
	"fmt"
	"strings"

	"github.com/shutx-net/micawber/core"
)

// keyKind names this store's key rules in a validation error.
const keyKind = "asset key"

// forbiddenChars are the characters Windows refuses in a filename.
//
// The colon earns its place twice over. core.NewAssetKey rejects one only in
// the first segment, where it would be a drive prefix; a colon in a later
// segment is an NTFS alternate data stream, so "logo.png:payload.exe" would be
// a hidden stream on "logo.png" rather than an object of its own.
const forbiddenChars = `<>:"|?*`

// checkKey applies the rules this store adds on top of [core.AssetKey], as pure
// string logic with no filesystem access.
//
// They are applied on every platform, not only on Windows, because the set of
// keys a store accepts must not depend on the host it runs on. An asset is
// referenced from a document that is committed to Git; a key that resolves on
// one machine and not on another breaks the content that is meant to be the
// source of truth, simply by being moved.
//
// The concrete hazard is silent data loss that reports success: on Windows a
// Put to "nul.png" writes to the null device, so the bytes vanish, the
// operation succeeds and Get then reports the object absent. Trailing dots and
// spaces are the same story with a twist — Win32 strips them, so "logo.png."
// and "logo.png" are one file there and two here.
//
// Nothing here repeats what core already refuses: control characters,
// backslashes, absolute and uncleaned paths, empty, "." and ".." segments and a
// drive prefix are all impossible by the time a key reaches this function.
//
// The returned error unwraps to [core.ErrInvalid].
func checkKey(key core.AssetKey) error {
	s := key.String()

	for _, seg := range strings.Split(s, "/") {
		switch {
		case strings.Trim(seg, " ") == "":
			return invalidKey(s, "has a segment of only spaces, which Windows cannot name")
		case strings.HasSuffix(seg, "."):
			return invalidKey(s, "has a segment %q ending in a dot, which Windows strips", seg)
		case strings.HasSuffix(seg, " "):
			return invalidKey(s, "has a segment %q ending in a space, which Windows strips", seg)
		}
		if i := strings.IndexAny(seg, forbiddenChars); i >= 0 {
			return invalidKey(s, "has a segment %q containing %q, which Windows forbids in a filename", seg, string(seg[i]))
		}
		if isReservedName(seg) {
			return invalidKey(s, "has a segment %q naming a Windows device, where a write would go to the device and be lost", seg)
		}
	}
	return nil
}

// invalidKey builds the validation error checkKey returns.
func invalidKey(key, format string, args ...any) error {
	return &core.ValidationError{
		Kind:   keyKind,
		Value:  key,
		Reason: fmt.Sprintf(format, args...),
	}
}

// isReservedName reports whether seg names a Windows device.
//
// The shape mirrors Go's own internal/filepathlite, so that this store and
// os.Root agree rather than each having a different idea of what is reserved:
// a device name may carry arbitrary trailing characters after a dot or a colon,
// and trailing spaces are ignored.
//
// The one deliberate divergence is that Go consults RtlIsDosDeviceName_U on
// Windows, which allows "CON.txt" from Windows 11 onwards. Doing the same here
// would make the set of keys a store accepts depend on a service pack, which is
// the problem this rule exists to solve.
func isReservedName(seg string) bool {
	base := seg
	if i := strings.IndexAny(base, ".:"); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	return isReservedBaseName(base)
}

// isReservedBaseName reports whether name is one of the device names itself,
// with no extension: CON, PRN, AUX, NUL, COM1-COM9, LPT1-LPT9, their superscript
// forms, and the two console handles CONIN$ and CONOUT$.
func isReservedBaseName(name string) bool {
	if len(name) == 3 {
		switch strings.ToUpper(name) {
		case "CON", "PRN", "AUX", "NUL":
			return true
		}
	}
	if len(name) >= 4 {
		switch strings.ToUpper(name[:3]) {
		case "COM", "LPT":
			if len(name) == 4 && name[3] >= '1' && name[3] <= '9' {
				return true
			}
			// Superscript ¹, ² and ³ are digits to Windows as well.
			switch name[3:] {
			case "¹", "²", "³":
				return true
			}
			return false
		}
	}
	if len(name) == 6 && strings.EqualFold(name, "CONIN$") {
		return true
	}
	if len(name) == 7 && strings.EqualFold(name, "CONOUT$") {
		return true
	}
	return false
}
