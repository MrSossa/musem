package claude

import (
	"strings"
	"unicode"
)

// sanitise strips control characters from text that came from outside musem and
// is destined for the screen.
//
// The dashboard renders session names, working directories and model
// identifiers verbatim, and every one of them is written by somebody else: the
// CLI reports whatever a directory happens to be called, and a transcript
// carries whatever model string was recorded in it. A terminal reads an escape
// sequence in any of those as an instruction — repositioning the cursor,
// rewriting the window title, or on emulators that honour OSC 52, replacing the
// system clipboard — so a directory named with one turns a status board into a
// surface somebody else controls. That it is re-rendered on every refresh is
// what makes it worth defending: the sequence fires continuously for as long as
// the session is listed.
//
// Stripping happens here rather than in the view because this is the boundary
// where foreign text becomes a musem value, and D8 puts every foreign-format
// concern in this package. A defence in the renderer would have to be repeated
// by every future view; one here holds for all of them.
//
// What survives: everything printable, plus spaces. Tabs and newlines are
// dropped rather than kept, because a single-line cell has no use for either and
// a newline is itself a layout instruction — it would shift every row below it.
// Removing rather than replacing is deliberate: a substitution character would
// give a crafted name a way to look like a legitimately odd one.
func sanitise(s string) string {
	if !strings.ContainsFunc(s, unprintable) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unprintable(r) {
			return -1
		}
		return r
	}, s)
}

// unprintable reports whether r would be an instruction to the terminal rather
// than a glyph on it. unicode.IsPrint covers C0 and C1 controls along with
// unassigned and format characters; the space it excludes is added back, since
// a directory name may legitimately contain one.
func unprintable(r rune) bool { return !unicode.IsPrint(r) && r != ' ' }
