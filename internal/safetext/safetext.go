// Package safetext strips from foreign text the characters a terminal reads as
// instructions rather than as glyphs.
//
// It exists because more than one adapter has the problem. The CLI reports
// session names and working directories, a transcript carries whatever model
// string was recorded in it, and git reports a branch name that whoever created
// the branch chose. All of it is written by somebody else and all of it is
// drawn on a terminal, where an escape sequence repositions the cursor, rewrites
// the window title, or on emulators that honour OSC 52 replaces the system
// clipboard. Every adapter needing the same defence and each keeping its own
// copy is how one of them ends up without it.
//
// Like execx, this wraps a piece of the standard library that is easy to get
// wrong and knows no musem type. A test keeps it a leaf.
package safetext

import (
	"strings"
	"unicode"
)

// Clean removes every character that is not a glyph, leaving the readable part
// of the value intact.
//
// What survives: everything printable, plus spaces. Tabs and newlines go with
// the rest, because a single-line cell has no use for either and a newline is
// itself a layout instruction — it would shift every row drawn below it.
// Removing rather than replacing is deliberate: a substitution character would
// give a crafted name a way to look like a legitimately odd one.
//
// What it removes is the character that makes a sequence a sequence, not the
// letters after it: "\x1b[2J" comes back as a literal "[2J", which the terminal
// draws as three harmless characters. The result is inert, not redacted.
func Clean(s string) string {
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

// HasUnprintable reports whether s contains anything Clean would remove.
//
// It exists for the values that must be refused rather than cleaned. Cleaning
// is right for text that exists to be read and wrong for a value that exists to
// be matched: rewriting an identifier gives it a different identity from the one
// its source uses, so the caller needs to be able to ask before it decides.
func HasUnprintable(s string) bool { return strings.ContainsFunc(s, unprintable) }

// unprintable reports whether r would be an instruction to the terminal rather
// than a glyph on it.
//
// unicode.IsPrint covers C0 and C1 controls along with unassigned and format
// characters; the space it excludes is added back, since a directory name may
// legitimately contain one. Including the format characters is not incidental:
// git refuses a ref name containing an ASCII control byte but accepts one
// containing U+202E, so a branch really can arrive carrying a direction
// override that reorders the text drawn around it.
func unprintable(r rune) bool { return !unicode.IsPrint(r) && r != ' ' }

// maxDetail bounds a line of foreign text on its way to the interface.
//
// A CLI that writes a stack trace to stderr must not be able to push the rest of
// the dashboard off the screen, and this text is drawn on every frame for as
// long as the failure lasts.
const maxDetail = 200

// FirstLine returns the first line of s that says something, cleaned and
// bounded.
//
// Three adapters need this: the Claude CLI, git and tmux all report a failure as
// prose on stderr, and all three want the same thing out of it — the first line
// that is not blank, made safe to draw and short enough not to take over the
// screen. Each keeping its own copy is the failure mode this package's own
// documentation warns about, one level up: not the character cleaning going
// missing, but the bound, or the rune-boundary cut, drifting apart between them.
//
// skip drops lines the caller knows are noise before the useful one — git
// announcing "Preparing worktree" before it explains what went wrong. Nil means
// skip nothing.
func FirstLine(s string, skip func(string) bool) string {
	for _, line := range strings.Split(s, "\n") {
		line = Clean(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		if skip != nil && skip(line) {
			continue
		}
		// Cut on a rune boundary, not a byte one. This text reaches a terminal,
		// where half a character is both unreadable and mis-measured by
		// everything that lays the line out.
		if runes := []rune(line); len(runes) > maxDetail {
			line = string(runes[:maxDetail]) + "…"
		}
		return line
	}
	return ""
}
