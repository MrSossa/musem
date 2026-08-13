package claude

import (
	"strings"
	"testing"

	"github.com/MrSossa/musem/internal/safetext"
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

// The CLI's own explanation of a failure is foreign text too, and the likelier
// vector of the two: a tool reporting an error tends to quote the input that
// caused it, and the inputs here are working directories that may come from a
// repository nobody vetted. It reaches the same terminal, on every frame, for as
// long as discovery keeps failing.
func TestTheCLIExplanationCannotCarryTerminalInstructions(t *testing.T) {
	detail := safetext.FirstLine("error: cannot open \x1b]0;owned\x07/p/\x1b[2Japi\n", nil)

	if strings.ContainsRune(detail, 0x1b) || strings.ContainsRune(detail, 0x07) {
		t.Errorf("detail = %q still carries a control character", detail)
	}
	if !strings.Contains(detail, "cannot open") || !strings.Contains(detail, "api") {
		t.Errorf("detail = %q lost the explanation it was meant to keep", detail)
	}
}

// What stripping removes is the escape, not the letters after it: "\x1b[2J" is
// disarmed to a literal "[2J", which the terminal draws as three harmless
// characters. That is the same bargain the session name strikes — the sequence
// stops being an instruction, and whatever remains is inert text rather than a
// redaction.
func TestADisarmedSequenceIsLeftAsInertText(t *testing.T) {
	detail := safetext.FirstLine("\x1b[2Jerror: the real reason", nil)

	if strings.ContainsRune(detail, 0x1b) {
		t.Errorf("detail = %q still carries the escape that made it a sequence", detail)
	}
	if !strings.Contains(detail, "error: the real reason") {
		t.Errorf("detail = %q lost the explanation", detail)
	}
}

// A line that is nothing but control bytes says nothing once they are gone, and
// an empty line is not an explanation. Falling through to the next one keeps a
// real message from being lost behind a crafted blank.
func TestAnExplanationOfPureControlBytesIsSkipped(t *testing.T) {
	if got := safetext.FirstLine("\x1b\x07\x00\nerror: the real reason\n", nil); got != "error: the real reason" {
		t.Errorf("firstLine = %q, want the first line that says something", got)
	}
}

// The identifier is the one foreign string that is refused rather than cleaned.
// It reaches the screen like the others — the detail pane always shows it, and
// the table falls back to it when a session has no name — but it is also the
// registry's key and the stem of the transcript filename, so a stripped id
// would name a session that does not exist and a file that cannot be opened.
func TestAnIdentifierCarryingControlBytesIsRefusedRatherThanCleaned(t *testing.T) {
	payload := []byte(`[{"sessionId":"s1","name":"good","status":"idle"},` +
		`{"sessionId":"\u001b]0;owned\u0007s2","name":"bad","status":"idle"}]`)

	discovery, err := parseAgents(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Sessions) != 1 || discovery.Sessions[0].ID != "s1" {
		t.Fatalf("sessions = %+v, want only the record with a usable identifier", discovery.Sessions)
	}
	// Counted, not merely dropped: an inventory quietly missing a session is the
	// failure the skipped count exists to make visible.
	if discovery.Skipped != 1 {
		t.Errorf("Skipped = %d, want the unusable record counted", discovery.Skipped)
	}
}
