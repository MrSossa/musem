package claude

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MrSossa/musem"
	"github.com/MrSossa/musem/internal/safetext"
)

// These tests run against fixtures captured from real CLI output. Their job is
// to fail loudly when the foreign format changes, which is the whole reason the
// adapter is isolated in one package.

func TestParseAgents(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "agents.json"))
	if err != nil {
		t.Fatal(err)
	}

	discovery, err := parseAgents(data)
	sessions := discovery.Sessions
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
	discovery, err := parseAgents(data)
	sessions := discovery.Sessions
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

	discovery, err := parseAgents(data)
	sessions := discovery.Sessions
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
	// Asserted as a resolved offset rather than a literal string: the cursor's
	// encoding is the reader's own business, the byte it resumes at is not.
	if offset, _, ok := parseCursor(first.Cursor); !ok || offset != int64(len(complete)) {
		t.Errorf("cursor %q resolves to offset %d, want %d: the fragment must not count as consumed",
			first.Cursor, offset, len(complete))
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
	if offset, _, ok := parseCursor(reading.Cursor); !ok || offset != int64(len(line)*2) {
		t.Errorf("cursor %q resolves to offset %d, want %d", reading.Cursor, offset, len(line)*2)
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

// A transcript that shrank was replaced, and the reading that follows starts at
// its beginning. That has to be announced: the consumer accumulates, so a
// silent restart adds the file's history to a total that already holds it.
func TestTruncatedTranscriptAnnouncesTheRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")

	line := func(model string, out int64) string {
		return `{"type":"assistant","message":{"model":"` + model +
			`","usage":{"input_tokens":1,"output_tokens":` +
			strconv.FormatInt(out, 10) + `}}}` + "\n"
	}

	if err := os.WriteFile(path, []byte(line("claude-opus-5", 10)+line("claude-opus-5", 20)), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewTranscriptReader()
	first, err := r.ReadNew(path, "")
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if first.Reset {
		t.Error("a first read is not a restart")
	}
	if len(first.Entries) != 2 {
		t.Fatalf("first read returned %d entries, want 2", len(first.Entries))
	}

	// The file is rotated: same name, shorter contents.
	if err := os.WriteFile(path, []byte(line("claude-opus-5", 5)), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := r.ReadNew(path, first.Cursor)
	if err != nil {
		t.Fatalf("read after truncation: %v", err)
	}
	if !second.Reset {
		t.Error("a transcript shorter than the cursor must report Reset, or its history is counted twice")
	}
	if len(second.Entries) != 1 {
		t.Errorf("read after truncation returned %d entries, want 1", len(second.Entries))
	}
}

// A resolved path is remembered so the search happens once. When the file
// behind it disappears the answer has to be dropped, or the session's cost is
// frozen for the rest of the process.
func TestVanishedTranscriptIsSearchedForAgain(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "project-a")
	second := filepath.Join(root, "project-b")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	entry := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	original := filepath.Join(first, "s1.jsonl")
	if err := os.WriteFile(original, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	r := &UsageReader{ProjectsDir: root, RetryLookupAfter: time.Minute}
	r.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := r.ReadUsage(ctx, "s1", ""); err != nil {
		t.Fatalf("first read: %v", err)
	}

	// The session's directory is renamed, which moves the transcript because the
	// tool encodes the working directory into its location.
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadUsage(ctx, "s1", ""); musem.ErrorCode(err) != musem.ENOTFOUND {
		t.Fatalf("read of a deleted transcript = %v, want ENOTFOUND", err)
	}

	moved := filepath.Join(second, "s1.jsonl")
	if err := os.WriteFile(moved, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}

	// Inside the backoff the miss is still remembered, which is the whole point
	// of remembering it: a session with no transcript must not re-scan every
	// project directory on every refresh.
	if _, err := r.ReadUsage(ctx, "s1", ""); musem.ErrorCode(err) != musem.ENOTFOUND {
		t.Errorf("read inside the retry backoff = %v, want ENOTFOUND", err)
	}

	now = now.Add(2 * time.Minute)
	reading, err := r.ReadUsage(ctx, "s1", "")
	if err != nil {
		t.Fatalf("read after the transcript moved: %v", err)
	}
	if len(reading.Entries) != 1 {
		t.Errorf("got %d entries after the move, want 1: the stale path was never dropped", len(reading.Entries))
	}
}

// A cursor that cannot be read leaves the file to be read from the start, and
// the caller holding totals built from the cursor that was lost. Adding a full
// re-read to those roughly doubles them, so this path has to announce the
// restart exactly as the truncation path does.
func TestUnreadableCursorAnnouncesTheRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	entry := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(entry+entry), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewTranscriptReader()

	for _, cursor := range []string{"not-a-number", "-17", "12,7"} {
		reading, err := r.ReadNew(path, cursor)
		if err != nil {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
		if !reading.Reset {
			t.Errorf("cursor %q: re-read the file from the start without reporting Reset, so its usage is counted twice",
				cursor)
		}
		if len(reading.Entries) != 2 {
			t.Errorf("cursor %q: got %d entries, want the whole file", cursor, len(reading.Entries))
		}
	}

	// The two cursors that legitimately mean something must not claim a restart.
	for _, cursor := range []string{"", CursorEnd} {
		reading, err := r.ReadNew(path, cursor)
		if err != nil {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
		if reading.Reset {
			t.Errorf("cursor %q: reported a restart, but nothing had been counted from a lost cursor", cursor)
		}
	}
}

// The payload belongs to another tool and can change shape without warning. One
// record musem cannot read must cost the user that session, not every session —
// the same degradation the transcript reader applies line by line.
func TestOneUnreadableAgentRecordDoesNotDiscardTheRest(t *testing.T) {
	// The middle record's startedAt has become a string, as a CLI change might
	// leave it.
	payload := []byte(`[
		{"sessionId":"a","name":"api","cwd":"/p/api","status":"running","pid":1,"startedAt":1700000000000},
		{"sessionId":"b","name":"web","cwd":"/p/web","status":"idle","pid":2,"startedAt":"2026-01-01T00:00:00Z"},
		{"sessionId":"c","name":"docs","cwd":"/p/docs","status":"idle","pid":3,"startedAt":1700000000000}
	]`)

	discovery, err := parseAgents(payload)
	sessions := discovery.Sessions
	if err != nil {
		t.Fatalf("one unreadable record failed the whole list: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want the 2 that could be read: %+v", len(sessions), sessions)
	}
	for _, want := range []string{"a", "c"} {
		var found bool
		for _, s := range sessions {
			if s.ID == want {
				found = true
			}
		}
		if !found {
			t.Errorf("session %q was discarded along with the record that could not be read", want)
		}
	}
	if discovery.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1: a record that was dropped has to be counted", discovery.Skipped)
	}
}

// The failure this exists to prevent: a field changing type across every record
// empties the list, and an empty list is indistinguishable from a machine with
// no sessions on it. The registry reads that as every session having ended and
// marks the lot, confidently and with nothing on screen to say otherwise.
func TestAPassThatCouldReadNothingSaysSoRatherThanReportingNoSessions(t *testing.T) {
	// Every record's startedAt has become a string, as a CLI release might leave
	// them all at once.
	payload := []byte(`[
		{"sessionId":"a","name":"api","status":"running","startedAt":"2026-01-01T00:00:00Z"},
		{"sessionId":"b","name":"web","status":"idle","startedAt":"2026-01-01T00:00:00Z"}
	]`)

	discovery, err := parseAgents(payload)
	if err != nil {
		t.Fatalf("the list itself parsed; only its records did not: %v", err)
	}
	if len(discovery.Sessions) != 0 {
		t.Fatalf("got %d sessions, want none readable", len(discovery.Sessions))
	}
	if discovery.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2; without the count this is indistinguishable from a machine running no sessions at all", discovery.Skipped)
	}
}

// A record with no session id is dropped for a different reason than one that
// would not decode, and is counted for the same reason: it is a session the user
// has that musem is not showing.
func TestARecordWithoutAnIdentifierIsCounted(t *testing.T) {
	discovery, err := parseAgents([]byte(`[{"name":"api","status":"running"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Sessions) != 0 || discovery.Skipped != 1 {
		t.Errorf("sessions = %d, skipped = %d; want 0 and 1", len(discovery.Sessions), discovery.Skipped)
	}
}

// A payload that is not a list at all is a different matter: there is nothing
// to degrade to, and saying so is better than reporting no sessions.
func TestAWhollyUnrecognisedPayloadIsStillAnError(t *testing.T) {
	if _, err := parseAgents([]byte(`{"sessions": []}`)); musem.ErrorCode(err) != musem.EUNPARSEABLE {
		t.Errorf("err = %v, want EUNPARSEABLE", err)
	}
}

// A transcript deleted and written again is a different file wearing the same
// name. Size alone cannot tell it from one that merely grew: replaced at or
// beyond the old offset, the read resumes mid-record and everything before that
// point is never counted.
func TestReplacedTranscriptIsDetectedEvenWhenItGrew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")

	entry := func(model string, out int64) string {
		return `{"type":"assistant","message":{"model":"` + model +
			`","usage":{"input_tokens":1,"output_tokens":` + strconv.FormatInt(out, 10) +
			`},"padding":"` + strings.Repeat("x", 400) + `"}}` + "\n"
	}

	original := entry("claude-opus-5", 1) + entry("claude-opus-5", 2)
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewTranscriptReader()
	first, err := r.ReadNew(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 2 {
		t.Fatalf("first read: %d entries, want 2", len(first.Entries))
	}

	// Replaced by a different, longer transcript for the same session.
	replacement := entry("claude-sonnet-5", 7) + entry("claude-sonnet-5", 8) + entry("claude-sonnet-5", 9)
	if len(replacement) <= len(original) {
		t.Fatal("the replacement must be longer, or this is the truncation case")
	}
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := r.ReadNew(path, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reset {
		t.Error("a replaced transcript must report Reset; resuming mid-file loses everything before the offset")
	}
	if len(second.Entries) != 3 {
		t.Errorf("got %d entries, want all 3 of the replacement", len(second.Entries))
	}
}

// Appending must never look like a replacement — the head of an append-only
// file does not change, and a fingerprint that moved with it would re-read the
// whole transcript on every pass.
func TestAppendingIsNotMistakenForReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")

	entry := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewTranscriptReader()
	cursor := ""
	total := 0
	restarts := 0

	// Grow the file well past the fingerprint window, one record at a time.
	// Totals are accumulated exactly as the accountant accumulates them, so a
	// restart that double-counts shows up here as it would in the real figure.
	for i := 0; i < 20; i++ {
		reading, err := r.ReadNew(path, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if reading.Reset {
			restarts++
			total = 0
		}
		total += len(reading.Entries)
		cursor = reading.Cursor

		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(entry); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// The file starts too short to identify, so exactly one restart is expected:
	// the pass on which its head first becomes hashable. More than that means
	// the fingerprint is moving with the file and every append looks like a
	// replacement.
	if restarts > 1 {
		t.Errorf("%d restarts across 20 appends, want at most the one where the head became hashable", restarts)
	}
	if total != 20 {
		t.Errorf("counted %d entries across 20 appends, want 20: records were counted more than once", total)
	}
}

// Two transcripts can carry one session identifier, and choosing between them
// by modification time oscillates: anything that touches the loser makes it the
// winner again, and every switch reads an unseen file from zero, reports a
// restart, and wipes the session's accumulated total.
//
// A resolved path is therefore kept while its file exists. The cost is a
// deliberate one: a session whose directory is renamed keeps being read from
// the transcript it was already reading, so its figure stops moving. A figure
// that stops is recoverable; one that is destroyed on every flip is not.
func TestResolvedPathSurvivesAnMtimeFlip(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "project-old")
	newDir := filepath.Join(root, "project-new")
	for _, d := range []string{oldDir, newDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	entry := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	oldPath := filepath.Join(oldDir, "s1.jsonl")
	if err := os.WriteFile(oldPath, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &UsageReader{ProjectsDir: root}
	ctx := context.Background()
	if _, err := r.ReadUsage(ctx, "s1", ""); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if got := r.paths["s1"]; got != oldPath {
		t.Fatalf("path = %q, want %q", got, oldPath)
	}

	// A second transcript appears for the same session, newer than the first.
	newPath := filepath.Join(newDir, "s1.jsonl")
	if err := os.WriteFile(newPath, []byte(entry+entry), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(newPath, later, later); err != nil {
		t.Fatal(err)
	}

	// Whichever file is touched, and however often, the answer does not move.
	for i := 0; i < 5; i++ {
		touch := oldPath
		if i%2 == 0 {
			touch = newPath
		}
		when := time.Now().Add(time.Duration(i+2) * time.Hour)
		if err := os.Chtimes(touch, when, when); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ReadUsage(ctx, "s1", ""); err != nil {
			t.Fatal(err)
		}
		if got := r.paths["s1"]; got != oldPath {
			t.Fatalf("pass %d: path flipped to %q; each flip wipes the session's total", i, got)
		}
	}

	// It moves only when the file it was reading is gone.
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadUsage(ctx, "s1", ""); err != nil {
		t.Fatalf("read after the resolved transcript was deleted: %v", err)
	}
	if got := r.paths["s1"]; got != newPath {
		t.Errorf("path = %q once the old transcript was deleted, want %q", got, newPath)
	}
}

// The CLI's own explanation is the actionable part of a discovery failure.
func TestDiscoveryFailureCarriesTheCLIExplanation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub relies on a POSIX shell")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\necho 'error: unknown option --json' >&2\nexit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	d := &Discoverer{Bin: bin, Timeout: 5 * time.Second}
	_, err := d.Discover(context.Background())
	if err == nil {
		t.Fatal("a non-zero exit must be an error")
	}
	if !strings.Contains(musem.ErrorMessage(err), "unknown option") {
		t.Errorf("message = %q; the CLI's own explanation was discarded", musem.ErrorMessage(err))
	}
}

// The delay that keeps a stranded grandchild from wedging the refresh loop also
// reports a CLI that did its job. The list was captured in full and the process
// exited zero; only something it forked and detached still held the pipe.
// Calling that a failure puts an error banner over stale rows on every refresh,
// for a call that worked every time.
func TestSessionsSurviveAChildThatOutlivesTheCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub relies on a POSIX shell")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	// Answers, then leaves a grandchild holding stdout open. The timeout is long
	// enough that nothing here is a timeout: what ends the call is WaitDelay.
	script := "#!/bin/sh\necho '[{\"sessionId\":\"s1\",\"name\":\"api\"}]'\nsleep 30 &\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	d := &Discoverer{Bin: bin, Timeout: 30 * time.Second}
	discovery, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("err = %v, want nil: the CLI answered, and only its child outlived it", err)
	}
	sessions := discovery.Sessions
	if len(sessions) != 1 || sessions[0].ID != "s1" {
		t.Errorf("sessions = %+v, want the one the CLI listed", sessions)
	}
}

// A transcript too short to fingerprint mints a cursor that cannot prove which
// file it came from. Resuming into a replacement on the strength of it skips
// everything before the offset and adds the rest to another file's totals.
func TestShortTranscriptReplacedByALongerOneIsDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	entry := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"

	// Deliberately unpadded: the file is under the fingerprint window, which is
	// exactly the case a padded fixture would hide.
	if err := os.WriteFile(path, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(entry) >= fingerprintBytes {
		t.Fatal("the fixture must be shorter than the fingerprint window")
	}

	r := NewTranscriptReader()
	first, err := r.ReadNew(path, "")
	if err != nil {
		t.Fatal(err)
	}

	replacement := strings.Repeat(entry, 12)
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := r.ReadNew(path, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reset {
		t.Error("a replacement of an unidentifiable file must report a restart, not resume into it")
	}
	if len(second.Entries) != 12 {
		t.Errorf("got %d entries, want all 12 of the replacement", len(second.Entries))
	}
}

// The cursor that carries accounting forward from elsewhere must not be
// re-read: its totals are already believed complete.
func TestCursorEndIsNeverTreatedAsUnverifiable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	entry := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(entry, 20)), 0o600); err != nil {
		t.Fatal(err)
	}

	reading, err := NewTranscriptReader().ReadNew(path, CursorEnd)
	if err != nil {
		t.Fatal(err)
	}
	if reading.Reset {
		t.Error("CursorEnd reported a restart; the carried-forward total would be wiped and the file recounted")
	}
	if len(reading.Entries) != 0 {
		t.Errorf("got %d entries, want 0", len(reading.Entries))
	}
}

// A file long enough to identify when the cursor was written, and too short to
// identify now, cannot be the same file. The size check alone misses this
// whenever the replacement lands between the old offset and the fingerprint
// window — the read then resumes into a stranger and adds it to another file's
// totals.
func TestTranscriptReplacedByAShorterOneIsDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	entry := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"

	// One complete record followed by a long unterminated fragment: the file is
	// past the fingerprint window, so the cursor carries one.
	if err := os.WriteFile(path, []byte(entry+strings.Repeat("x", 600)), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewTranscriptReader()
	first, err := r.ReadNew(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, fp, _ := parseCursor(first.Cursor); fp == "" {
		t.Fatal("the fixture must produce a cursor carrying a fingerprint")
	}

	// Replaced by a different transcript that is shorter than the window but
	// longer than the offset the cursor holds.
	replacement := entry + entry
	if len(replacement) >= fingerprintBytes {
		t.Fatal("the replacement must be shorter than the fingerprint window")
	}
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := r.ReadNew(path, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reset {
		t.Error("a file that stopped being identifiable was resumed into rather than restarted")
	}
	if len(second.Entries) != 2 {
		t.Errorf("got %d entries, want both records of the replacement", len(second.Entries))
	}
}

// The CLI's message reaches the dashboard header, where half a character is
// both unreadable and mis-measured by everything that lays the line out.
func TestCLIDetailIsTruncatedOnARuneBoundary(t *testing.T) {
	got := safetext.FirstLine(strings.Repeat("→", 400), nil)

	if !utf8.ValidString(got) {
		t.Errorf("FirstLine produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated message must say so: %q", got)
	}
}

// The cache-creation split is trusted only as far as it accounts for the total
// the same record reports. A time-to-live tier this reader has never heard of
// would otherwise vanish from both the token count and the bill — silently,
// with nothing marked, which is the one outcome the design rules out.
func TestUnaccountedCacheTierIsNotDroppedSilently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")

	// The split names 100 + 200, but the record's own total is 500: 200 tokens
	// belong to a tier this reader does not know about.
	line := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{` +
		`"input_tokens":0,"output_tokens":0,` +
		`"cache_creation_input_tokens":500,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	reading, err := NewTranscriptReader().ReadNew(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(reading.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(reading.Entries))
	}

	got := reading.Entries[0].Usage
	if total := got.CacheWriteTokens(); total != 500 {
		t.Errorf("counted %d cache-creation tokens, want the 500 the record reports: "+
			"%d went missing with no marker", total, 500-total)
	}
	// The shortfall lands on the cheaper tier, so the money understates rather
	// than inflating — the same trade the older-record path makes.
	if got.CacheWrite1hTokens != 200 {
		t.Errorf("1h tier = %d, want the 200 the split names", got.CacheWrite1hTokens)
	}
}

// A cursor descended from CursorEnd carries a total that was never derived from
// reading this file, so re-reading cannot reproduce it. The restart rules must
// defer to that lineage even after the file becomes identifiable, or the
// carried-forward history CursorEnd exists to protect is destroyed.
func TestCursorEndLineageSurvivesTheFileBecomingIdentifiable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	entry := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"

	// Short enough to have no fingerprint, which is the window the loss needs.
	if err := os.WriteFile(path, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(entry) >= fingerprintBytes {
		t.Fatal("the fixture must be shorter than the fingerprint window")
	}

	r := NewTranscriptReader()
	adopted, err := r.ReadNew(path, CursorEnd)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Reset || len(adopted.Entries) != 0 {
		t.Fatalf("adopting the file counted %d entries (reset=%v), want none",
			len(adopted.Entries), adopted.Reset)
	}

	// The transcript grows past the point where it can be identified.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Repeat(entry, 10)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	next, err := r.ReadNew(path, adopted.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if next.Reset {
		t.Error("the adopted cursor was treated as re-readable; the carried-forward total is wiped and the file recounted")
	}
	if len(next.Entries) != 10 {
		t.Errorf("got %d entries, want the 10 appended after adoption", len(next.Entries))
	}
}

// A session identifier is pasted into a glob pattern, where a separator or a
// metacharacter stops meaning itself. Both are refused at the edge rather than
// resolved to a plausible wrong file and counted against the session.
func TestUsageReaderRejectsIdentifiersThatEscapeTheSearch(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}

	line := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":9}}}` + "\n"
	if err := os.WriteFile(filepath.Join(project, "real.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	// A transcript outside the projects root, which no identifier may reach.
	if err := os.WriteFile(filepath.Join(root, "outside.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{
		"",           // nothing to search for
		"../outside", // climbs out of the projects root
		"a/b",        // a separator by any other route
		"*",          // matches whatever transcript happens to be first
		"rea?",       // ditto, one character at a time
		"[r]eal",     // a character class
	} {
		t.Run(id, func(t *testing.T) {
			r := &UsageReader{ProjectsDir: root}
			reading, err := r.ReadUsage(context.Background(), id, "")
			if err == nil {
				t.Fatalf("ReadUsage(%q) succeeded, reading = %+v", id, reading)
			}
			if got := musem.ErrorCode(err); got != musem.EINVALID {
				t.Errorf("ReadUsage(%q) code = %q, want %q", id, got, musem.EINVALID)
			}
			if len(reading.Entries) != 0 {
				t.Errorf("ReadUsage(%q) returned %d entries; nothing may be counted", id, len(reading.Entries))
			}
		})
	}

	// The ordinary case still resolves, so the check narrows nothing it should not.
	r := &UsageReader{ProjectsDir: root}
	reading, err := r.ReadUsage(context.Background(), "real", "")
	if err != nil {
		t.Fatalf("ReadUsage(\"real\"): %v", err)
	}
	if len(reading.Entries) != 1 {
		t.Errorf("a well-formed identifier must still resolve, got %+v", reading)
	}
}
