package localfs

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// reservedBaseNames is every Windows device name the store refuses, mirroring
// Go's own list in internal/filepathlite so that the store and os.Root agree
// rather than each having a different idea of what is reserved.
var reservedBaseNames = []string{
	"CON", "PRN", "AUX", "NUL",
	"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
	"COM¹", "COM²", "COM³",
	"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
	"LPT¹", "LPT²", "LPT³",
	"CONIN$", "CONOUT$",
}

// TestKeyRejectsWindowsReservedNamesInEverySegment covers the rule that stops
// silent data loss: on Windows a Put to "nul.png" writes to the null device,
// the bytes vanish and the operation reports success.
//
// The rule is applied on every platform, in every segment, bare and with an
// extension, in any casing, because the set of keys a store accepts must not
// depend on the host it runs on. An asset referenced from a document in Git has
// to resolve on every machine that checks the repository out.
func TestKeyRejectsWindowsReservedNamesInEverySegment(t *testing.T) {
	for _, base := range reservedBaseNames {
		for _, name := range []string{base, strings.ToLower(base), base + ".png", strings.ToLower(base) + ".PnG", base + ".tar.gz"} {
			for _, key := range []string{name, "img/" + name, name + "/logo.png", "a/" + name + "/b.png"} {
				t.Run(key, func(t *testing.T) {
					err := checkKey(mustKey(t, key))
					if !errors.Is(err, core.ErrInvalid) {
						t.Errorf("checkKey(%q) = %v, want an error matching core.ErrInvalid", key, err)
					}
				})
			}
		}
	}
}

// TestKeyRejectsTrailingDotsAndSpaces covers the other half of the Windows
// filename rule. Win32 strips a trailing dot or space, so "logo.png." and
// "logo.png" are one file there and two here: without the rule the store would
// report two objects on one host and one on another.
func TestKeyRejectsTrailingDotsAndSpaces(t *testing.T) {
	keys := []string{
		"logo.png.",
		"logo.png ",
		" ",
		"   ",
		"img/logo.png.",
		"img/logo.png ",
		"img./logo.png",
		"img /logo.png",
		"  /logo.png",
	}

	for _, key := range keys {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			err := checkKey(mustKey(t, key))
			if !errors.Is(err, core.ErrInvalid) {
				t.Errorf("checkKey(%q) = %v, want an error matching core.ErrInvalid", key, err)
			}
		})
	}
}

// TestKeyRejectsCharactersWindowsForbids covers the characters that are simply
// illegal in a Windows filename, and the colon in particular.
//
// core.NewAssetKey rejects a colon only in the first segment, where it would be
// a drive prefix. A colon in a later segment is an NTFS alternate data stream,
// so "logo.png:payload.exe" would be a hidden stream on "logo.png" rather than
// an object of its own.
func TestKeyRejectsCharactersWindowsForbids(t *testing.T) {
	for _, char := range []string{"<", ">", ":", `"`, "|", "?", "*"} {
		for _, key := range []string{"img/logo" + char + ".png", "img/a" + char + "b/logo.png", "img/" + char} {
			t.Run(key, func(t *testing.T) {
				err := checkKey(mustKey(t, key))
				if !errors.Is(err, core.ErrInvalid) {
					t.Errorf("checkKey(%q) = %v, want an error matching core.ErrInvalid", key, err)
				}
			})
		}
	}
}

// TestKeyAcceptsOrdinaryKeys is the counterweight. A rule that only ever
// rejects is easy to widen by accident, so the keys that must keep working are
// asserted too: "console.png" begins with CON and is not CON, "lpt10.png" is
// one digit past the reserved range, and a dot inside a segment is ordinary.
func TestKeyAcceptsOrdinaryKeys(t *testing.T) {
	keys := []string{
		"logo.png",
		"img/logo.png",
		"img/2026/08/hero.jpg",
		"console.png",
		"common/logo.png",
		"nulled.png",
		"lpt10.png",
		"com0.png",
		"com10.png",
		"conin.png",
		"prnt.png",
		"a.b.c.png",
		"-leading-dash.png",
		"file with spaces.png",
		"日本語/写真.jpg",
		"emoji-🙂.png",
		"deep/a/b/c/d/e/f/g/h/i/j/k.png",
		strings.Repeat("x", 255) + "/" + strings.Repeat("y", 251) + ".png",
	}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			if err := checkKey(mustKey(t, key)); err != nil {
				t.Errorf("checkKey(%q) = %v, want nil", key, err)
			}
		})
	}
}
