package safetext

import "testing"

func TestCleanPreservesOrdinaryText(t *testing.T) {
	// Spaces, accents and wide glyphs are ordinary in a directory name and must
	// survive untouched; only what the terminal would read as an instruction goes.
	for _, s := range []string{"api", "/home/dev/my projects/café", "作業ディレクトリ", ""} {
		if got := Clean(s); got != s {
			t.Errorf("Clean(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Newlines and tabs are dropped rather than kept as whitespace: a cell is one
// line, and a newline in it would shift every row drawn below.
func TestCleanDropsLineBreaksAndTabs(t *testing.T) {
	if got := Clean("api\nweb\tdocs"); got != "apiwebdocs" {
		t.Errorf("Clean = %q, want the layout characters gone", got)
	}
}

func TestCleanRemovesTheCharacterThatDrivesTheTerminal(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"api\x1b]0;owned\x07", "api]0;owned"},
		{"\x1b[2Jreal", "[2Jreal"},
		{"a\x00b\x7fc", "abc"},
		// C1 controls arrive as single runes once decoded, and are as much an
		// instruction as their C0 equivalents.
		{"a\u0085b", "ab"},
	} {
		if got := Clean(tc.in); got != tc.want {
			t.Errorf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The class a control-byte check misses, and the reason the filter is IsPrint
// rather than a control-character test: these reorder or hide the text drawn
// around them without being controls at all.
func TestCleanRemovesDirectionOverrides(t *testing.T) {
	for _, r := range []string{"\u202e", "\u202d", "\u2066", "\u200b"} {
		in := "feat" + r + "main"
		if got := Clean(in); got != "featmain" {
			t.Errorf("Clean(%q) = %q, want the override gone", in, got)
		}
	}
}

// HasUnprintable is what the callers that must refuse rather than clean ask,
// and it has to agree with Clean about every rune or the two defences disagree
// about the same value.
func TestHasUnprintableAgreesWithClean(t *testing.T) {
	for _, s := range []string{"api", "", "作業", "a\x1bb", "feat\u202emain", "a\nb", "with space"} {
		if got, want := HasUnprintable(s), Clean(s) != s; got != want {
			t.Errorf("HasUnprintable(%q) = %v but Clean %s it", s, got, map[bool]string{true: "changed", false: "left"}[want])
		}
	}
}
