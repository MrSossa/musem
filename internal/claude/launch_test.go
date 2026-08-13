package claude

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MrSossa/musem"
)

func fakeClaude(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub relies on a POSIX shell")
	}

	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// R5: the command carries the identifier musem chose, so the session it starts
// is the session it will later discover.
func TestTheAgentCommandCarriesTheChosenSessionID(t *testing.T) {
	a := &Agent{Bin: "/opt/claude"}

	got := a.Command("6ba7b810-9dad-41d1-80b4-00c04fd430c8")
	want := []string{"/opt/claude", "--session-id", "6ba7b810-9dad-41d1-80b4-00c04fd430c8"}

	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("command = %v, want %v", got, want)
	}
}

// The CLI accepts a UUID and nothing else, so an identifier that is not one
// would fail at the point where a worktree has already been created.
var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestGeneratedSessionIDsAreUUIDs(t *testing.T) {
	a := NewAgent()

	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		id, err := a.NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID: %v", err)
		}
		if !uuidV4.MatchString(id) {
			t.Fatalf("id = %q, which the CLI would refuse", id)
		}
		if seen[id] {
			t.Fatalf("id %q was generated twice; two launches would share a session", id)
		}
		seen[id] = true
	}
}

// R7, scenario "No session substrate" applied to the agent tool: an absent CLI
// is named rather than reported as a generic failure, and it is found before a
// launch has created anything.
func TestAMissingAgentToolIsReportedAsUnavailable(t *testing.T) {
	a := &Agent{Bin: filepath.Join(t.TempDir(), "definitely-not-here"), Timeout: time.Second}

	err := a.Available(context.Background())
	if err == nil {
		t.Fatal("a missing Claude CLI reported itself as available")
	}
	if got := musem.ErrorCode(err); got != musem.EUNAVAILABLE {
		t.Errorf("code = %q, want %q", got, musem.EUNAVAILABLE)
	}
	if msg := musem.ErrorMessage(err); !strings.Contains(msg, "Claude CLI") || !strings.Contains(msg, "PATH") {
		t.Errorf("message = %q, want it to name the CLI and PATH", msg)
	}
}

func TestAnInstalledAgentToolReportsNoError(t *testing.T) {
	a := &Agent{Bin: fakeClaude(t, "echo '1.0.0'"), Timeout: 5 * time.Second}

	if err := a.Available(context.Background()); err != nil {
		t.Fatalf("Available: %v", err)
	}
}

// A CLI that is installed but refuses to run is still a reason not to launch,
// and is still reported as a source problem rather than an internal one.
func TestAnAgentToolThatRefusesToRunIsUnavailable(t *testing.T) {
	a := &Agent{Bin: fakeClaude(t, "echo 'not logged in' >&2; exit 1"), Timeout: 5 * time.Second}

	err := a.Available(context.Background())
	if err == nil {
		t.Fatal("a CLI that exits non-zero reported itself as available")
	}
	if got := musem.ErrorCode(err); got != musem.EUNAVAILABLE {
		t.Errorf("code = %q, want %q", got, musem.EUNAVAILABLE)
	}
}
