package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MrSossa/musem"
)

// These tests run against fixtures captured from real CLI output. Their job is
// to fail loudly when the foreign format changes, which is the whole reason the
// adapter is isolated in one package.

func TestParseAgents(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "agents.json"))
	if err != nil {
		t.Fatal(err)
	}

	sessions, err := parseAgents(data)
	if err != nil {
		t.Fatalf("parseAgents: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(sessions))
	}

	first := sessions[0]
	if first.ID != "27fa386e-4411-40aa-8daf-47a6a21cbc7b" {
		t.Errorf("ID = %q", first.ID)
	}
	if first.Dir != "/home/dev/projects/alpha" {
		t.Errorf("Dir = %q", first.Dir)
	}
	if first.Status != musem.StatusIdle {
		t.Errorf("Status = %q, want idle", first.Status)
	}
	if first.Started.IsZero() {
		t.Error("Started was not populated from startedAt")
	}
	if err := first.Validate(); err != nil {
		t.Errorf("parsed session fails domain validation: %v", err)
	}

	if sessions[1].Status != musem.StatusRunning {
		t.Errorf("busy should map to running, got %q", sessions[1].Status)
	}
	if sessions[2].Status != musem.StatusWaiting {
		t.Errorf("waiting should map to waiting, got %q", sessions[2].Status)
	}
}

// Two sessions in the same directory must stay two entries: the working
// directory is not an identity.
func TestParseAgentsKeepsSessionsSharingADirectory(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := parseAgents(data)
	if err != nil {
		t.Fatal(err)
	}

	var inAlpha int
	for _, s := range sessions {
		if s.Dir == "/home/dev/projects/alpha" {
			inAlpha++
		}
	}
	if inAlpha != 2 {
		t.Errorf("got %d sessions in the shared directory, want 2", inAlpha)
	}
}

// Unknown fields must be tolerated and an unknown status must degrade to
// indeterminate rather than being guessed at.
func TestParseAgentsToleratesUnknownShape(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "agents_unknown_fields.json"))
	if err != nil {
		t.Fatal(err)
	}

	sessions, err := parseAgents(data)
	if err != nil {
		t.Fatalf("unknown fields must not fail the parse: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].Status != musem.StatusIdle {
		t.Errorf("known status alongside unknown fields = %q", sessions[0].Status)
	}
	if sessions[1].Status != musem.StatusIndeterminate {
		t.Errorf("unknown status = %q, want indeterminate", sessions[1].Status)
	}
}

func TestParseAgentsRejectsGarbage(t *testing.T) {
	_, err := parseAgents([]byte("this is not json"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if code := musem.ErrorCode(err); code != musem.EUNPARSEABLE {
		t.Errorf("code = %q, want %q", code, musem.EUNPARSEABLE)
	}
}

func TestReadTranscript(t *testing.T) {
	r := NewTranscriptReader()
	path := filepath.Join("testdata", "transcript.jsonl")

	usage, skipped, err := r.ReadNew(path)
	if err != nil {
		t.Fatalf("ReadNew: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(usage) != 3 {
		t.Fatalf("got %d usage entries, want 3", len(usage))
	}

	first := usage[0]
	if first.Model != "claude-opus-5" {
		t.Errorf("Model = %q", first.Model)
	}
	// Cache creation must survive as its own figure, split by time-to-live: the
	// two tiers are priced differently, so collapsing them misreports the bill.
	if first.Usage.CacheWrite1hTokens != 26177 {
		t.Errorf("CacheWrite1hTokens = %d, want 26177", first.Usage.CacheWrite1hTokens)
	}
	if first.Usage.CacheWrite5mTokens != 0 {
		t.Errorf("CacheWrite5mTokens = %d, want 0", first.Usage.CacheWrite5mTokens)
	}
	if first.Usage.InputTokens != 2 || first.Usage.OutputTokens != 109 {
		t.Errorf("input/output = %d/%d, want 2/109", first.Usage.InputTokens, first.Usage.OutputTokens)
	}
	if usage[1].Usage.CacheReadTokens != 26177 {
		t.Errorf("CacheReadTokens = %d, want 26177", usage[1].Usage.CacheReadTokens)
	}
	if usage[2].Model != "claude-haiku-4-5-20251001" {
		t.Errorf("third model = %q", usage[2].Model)
	}
}

// Older records carry only the cache-creation total with no time-to-live split.
// Attributing it to the cheaper tier understates rather than inflates the bill.
func TestCacheWriteWithoutSplitFallsBackToFiveMinutes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	line := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"cache_creation_input_tokens":900,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	usage, _, err := NewTranscriptReader().ReadNew(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 {
		t.Fatalf("got %d entries, want 1", len(usage))
	}
	if usage[0].Usage.CacheWrite5mTokens != 900 {
		t.Errorf("CacheWrite5mTokens = %d, want 900", usage[0].Usage.CacheWrite5mTokens)
	}
	if usage[0].Usage.CacheWrite1hTokens != 0 {
		t.Errorf("CacheWrite1hTokens = %d, want 0", usage[0].Usage.CacheWrite1hTokens)
	}
}

// A second read must return only what was appended since the first.
func TestReadTranscriptIsIncremental(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	original, err := os.ReadFile(filepath.Join("testdata", "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewTranscriptReader()
	if _, _, err := r.ReadNew(path); err != nil {
		t.Fatal(err)
	}

	again, _, err := r.ReadNew(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("re-reading an unchanged file returned %d entries, want 0", len(again))
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	appended, _, err := r.ReadNew(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(appended) != 1 {
		t.Fatalf("got %d entries after appending one, want 1", len(appended))
	}
	if appended[0].Usage.OutputTokens != 2 {
		t.Errorf("OutputTokens = %d, want 2", appended[0].Usage.OutputTokens)
	}
}

// A truncated or replaced file must be re-read from the start rather than
// resumed at a now-meaningless offset.
func TestReadTranscriptHandlesTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	line := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"

	if err := os.WriteFile(path, []byte(line+line+line), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewTranscriptReader()
	if _, _, err := r.ReadNew(path); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	usage, _, err := r.ReadNew(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 {
		t.Fatalf("got %d entries after truncation, want 1", len(usage))
	}
}

// One bad line must not cost the caller the good ones around it.
func TestReadTranscriptSkipsMalformedLines(t *testing.T) {
	r := NewTranscriptReader()
	usage, skipped, err := r.ReadNew(filepath.Join("testdata", "transcript_malformed.jsonl"))
	if err != nil {
		t.Fatalf("malformed lines must not fail the read: %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("got %d usable entries, want 2", len(usage))
	}
	if skipped == 0 {
		t.Error("skipped = 0; malformed lines must be counted so the loss is visible")
	}
}

func TestReadTranscriptMissingFile(t *testing.T) {
	r := NewTranscriptReader()
	_, _, err := r.ReadNew(filepath.Join("testdata", "does-not-exist.jsonl"))
	if code := musem.ErrorCode(err); code != musem.ENOTFOUND {
		t.Errorf("code = %q, want %q", code, musem.ENOTFOUND)
	}
}

// A permanently broken transcript must produce one diagnostic, not one per
// refresh cycle.
func TestShouldWarnOnlyOnce(t *testing.T) {
	r := NewTranscriptReader()
	if !r.ShouldWarn("a.jsonl") {
		t.Error("first warning should be allowed")
	}
	if r.ShouldWarn("a.jsonl") {
		t.Error("second warning for the same path should be suppressed")
	}
	if !r.ShouldWarn("b.jsonl") {
		t.Error("a different path should warn independently")
	}
}
