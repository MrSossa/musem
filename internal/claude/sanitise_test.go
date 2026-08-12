package claude

import (
	"strings"
	"testing"
)

// The dashboard redraws every refresh, so a name carrying an escape sequence is
// not a one-off glitch: it is an instruction re-issued to the terminal for as
// long as the session is listed.
//
// The escapes arrive \u-encoded because that is the only form that reaches a
// musem value: a raw ESC byte inside a JSON string is invalid, and the decoder
// rejects the whole payload before any of it becomes a session.
func TestForeignTextCannotCarryTerminalInstructions(t *testing.T) {
	payload := []byte(`[{"sessionId":"s1","name":"api\u001b]0;owned\u0007","cwd":"/p/\u001b[2Japi","status":"idle"}]`)

	discovery, err := parseAgents(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(discovery.Sessions))
	}

	s := discovery.Sessions[0]
	if strings.ContainsRune(s.Name, 0x1b) || strings.ContainsRune(s.Name, 0x07) {
		t.Errorf("Name = %q still carries a control character", s.Name)
	}
	if strings.ContainsRune(s.Dir, 0x1b) {
		t.Errorf("Dir = %q still carries a control character", s.Dir)
	}
	// The readable part survives: stripping is a defence, not a redaction, and a
	// session the user has to recognise by name must stay recognisable.
	if !strings.Contains(s.Name, "api") {
		t.Errorf("Name = %q lost the text it was meant to keep", s.Name)
	}
	if !strings.Contains(s.Dir, "/p/") || !strings.Contains(s.Dir, "api") {
		t.Errorf("Dir = %q lost the path it was meant to keep", s.Dir)
	}
}

func TestSanitisePreservesOrdinaryText(t *testing.T) {
	// Spaces, accents and wide glyphs are ordinary in a directory name and must
	// survive untouched; only what the terminal would read as an instruction goes.
	for _, s := range []string{"api", "/home/dev/my projects/café", "作業ディレクトリ", ""} {
		if got := sanitise(s); got != s {
			t.Errorf("sanitise(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Newlines and tabs are dropped rather than kept as whitespace: a cell is one
// line, and a newline in it would shift every row drawn below.
func TestSanitiseDropsLineBreaksAndTabs(t *testing.T) {
	if got := sanitise("api\nweb\tdocs"); got != "apiwebdocs" {
		t.Errorf("sanitise = %q, want the layout characters gone", got)
	}
}
