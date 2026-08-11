package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

	reading, err := r.ReadNew(path, "")
	usage, skipped := reading.Entries, reading.Skipped
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

	reading, err := NewTranscriptReader().ReadNew(path, "")
	usage := reading.Entries
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
	first, err := r.ReadNew(path, "")
	if err != nil {
		t.Fatal(err)
	}

	again, err := r.ReadNew(path, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Entries) != 0 {
		t.Fatalf("re-reading an unchanged file returned %d entries, want 0", len(again.Entries))
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

	appended, err := r.ReadNew(path, again.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(appended.Entries) != 1 {
		t.Fatalf("got %d entries after appending one, want 1", len(appended.Entries))
	}
	if appended.Entries[0].Usage.OutputTokens != 2 {
		t.Errorf("OutputTokens = %d, want 2", appended.Entries[0].Usage.OutputTokens)
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
	full, err := r.ReadNew(path, "")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	reading, err := r.ReadNew(path, full.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(reading.Entries) != 1 {
		t.Fatalf("got %d entries after truncation, want 1", len(reading.Entries))
	}
}

// One bad line must not cost the caller the good ones around it.
func TestReadTranscriptSkipsMalformedLines(t *testing.T) {
	r := NewTranscriptReader()
	reading, err := r.ReadNew(filepath.Join("testdata", "transcript_malformed.jsonl"), "")
	if err != nil {
		t.Fatalf("malformed lines must not fail the read: %v", err)
	}
	if len(reading.Entries) != 2 {
		t.Fatalf("got %d usable entries, want 2", len(reading.Entries))
	}
	if reading.Skipped == 0 {
		t.Error("skipped = 0; malformed lines must be counted so the loss is visible")
	}
}

func TestReadTranscriptMissingFile(t *testing.T) {
	r := NewTranscriptReader()
	_, err := r.ReadNew(filepath.Join("testdata", "does-not-exist.jsonl"), "")
	if code := musem.ErrorCode(err); code != musem.ENOTFOUND {
		t.Errorf("code = %q, want %q", code, musem.ENOTFOUND)
	}
}

// musem reads transcripts while the agent is still writing them, so a read
// landing mid-line is routine. The fragment must stay uncounted until the
// writer finishes it — counting it would step over the completed record and
// lose its usage for good.
func TestReadTranscriptResumesAcrossAPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	complete := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	// The second record is caught halfway through being written.
	fragment := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_`
	rest := `tokens":7,"output_tokens":9}}}` + "\n"

	if err := os.WriteFile(path, []byte(complete+fragment), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewTranscriptReader()
	first, err := r.ReadNew(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 1 {
		t.Fatalf("got %d entries, want the 1 complete record", len(first.Entries))
	}
	if want := formatCursor(int64(len(complete))); first.Cursor != want {
		t.Errorf("cursor = %q, want %q: the fragment must not count as consumed", first.Cursor, want)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(rest); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	second, err := r.ReadNew(path, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 1 {
		t.Fatalf("got %d entries once the line was completed, want 1", len(second.Entries))
	}
	if got := second.Entries[0].Usage.InputTokens; got != 7 {
		t.Errorf("InputTokens = %d, want 7", got)
	}
}

// A transcript written with CRLF endings must parse, and must not have its
// lines re-read — under-counting the newline would resume mid-record and count
// the same usage twice.
func TestReadTranscriptHandlesCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	line := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\r\n"

	if err := os.WriteFile(path, []byte(line+line), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewTranscriptReader()
	first, err := r.ReadNew(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(first.Entries))
	}
	if first.Skipped != 0 {
		t.Errorf("skipped = %d; CRLF endings are not malformed", first.Skipped)
	}

	again, err := r.ReadNew(path, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Entries) != 0 {
		t.Errorf("re-reading returned %d entries; the cursor under-counted the line endings", len(again.Entries))
	}
}

// A cursor carried forward from accounting that predates cursors resumes at the
// end of the file: the stored total is already believed complete, and reading
// the file again would add its whole history to itself.
func TestCursorEndCountsNothingAlreadyWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	line := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"

	if err := os.WriteFile(path, []byte(line+line), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewTranscriptReader()
	reading, err := r.ReadNew(path, CursorEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(reading.Entries) != 0 {
		t.Fatalf("got %d entries, want 0: everything already written is already counted", len(reading.Entries))
	}
	if want := formatCursor(int64(len(line) * 2)); reading.Cursor != want {
		t.Errorf("cursor = %q, want %q", reading.Cursor, want)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	next, err := r.ReadNew(path, reading.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Entries) != 1 {
		t.Errorf("got %d entries appended after the handover, want 1", len(next.Entries))
	}
}

// A session seen before its transcript exists must not be given up on, but a
// session whose transcript is gone must not be searched for on every refresh.
func TestUsageReaderRetriesAFailedLookupOnlyAfterTheInterval(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "project"), 0o700); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	r := NewUsageReader()
	r.ProjectsDir = dir
	r.RetryLookupAfter = time.Minute
	r.now = func() time.Time { return now }

	if _, err := r.resolve("s1"); err == nil {
		t.Fatal("expected a miss for a session with no transcript")
	}

	// The transcript appears, but the miss is still remembered.
	path := filepath.Join(dir, "project", "s1.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolve("s1"); err == nil {
		t.Error("a failed lookup must be remembered rather than repeated on every refresh")
	}

	now = now.Add(2 * time.Minute)
	got, err := r.resolve("s1")
	if err != nil {
		t.Fatalf("the lookup must be retried once the interval has passed: %v", err)
	}
	if got != path {
		t.Errorf("resolved %q, want %q", got, path)
	}
}

// Setting only the exported fields must produce a working reader; a struct
// literal that panics is a trap for anyone who does not read the constructor.
func TestUsageReaderIsUsableAsAStructLiteral(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":3}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "project", "s1.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &UsageReader{ProjectsDir: dir}
	reading, err := r.ReadUsage(context.Background(), "s1", "")
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	if len(reading.Entries) != 1 || reading.Entries[0].Usage.InputTokens != 3 {
		t.Errorf("reading = %+v, want one entry of 3 input tokens", reading)
	}
}
