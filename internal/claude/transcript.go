package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MrSossa/musem"
)

// transcriptLine is the subset of a JSONL entry musem cares about. A transcript
// carries many record types; only assistant responses report usage.
type transcriptLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Model string `json:"model"`
		Usage *struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`

			// CacheCreation splits the creation total by time-to-live. The two
			// are billed at different rates, so the split is what makes an
			// accurate cost possible.
			CacheCreation *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// TranscriptReader reads usage from a session's JSONL transcript, resuming from
// a cursor the caller supplies.
//
// Transcripts are append-only, which is what makes incremental reading safe: a
// file that only grows can be resumed from a byte offset. The offset is handed
// back to the caller rather than kept here, so it can be persisted in the same
// write as the totals derived from it — an offset that outlives the process but
// a total that does not, or the reverse, is what makes a restart recount.
//
// The reader holds no state at all, which is what makes it safe to share.
type TranscriptReader struct{}

// NewTranscriptReader returns a reader.
func NewTranscriptReader() *TranscriptReader { return &TranscriptReader{} }

// ReadNew returns the usage recorded in path since cursor, together with the
// cursor to resume from next time.
//
// An empty cursor reads from the start. Lines that cannot be understood are
// skipped rather than aborting the read: a single malformed record must not
// cost the caller every other figure in the file. The count of skipped lines is
// reported so the caller can surface that something was lost.
func (r *TranscriptReader) ReadNew(path, cursor string) (musem.UsageReading, error) {
	// Opening a variable path is the point of this function, so gosec's warning
	// cannot be designed away — it can only be answered. The path is not supplied
	// by a caller who chose it: it is a match returned by the glob in
	// UsageReader.resolve, rooted at the projects directory, and the identifier
	// that glob is built from has already been refused if it carries a separator
	// or a pattern character. See checkSessionID, which exists for this reason.
	// The file is opened read-only and parsed line by line; nothing here is
	// executed and nothing is written back.
	// #nosec G304 -- path is a glob match under the projects root, not caller-chosen
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return musem.UsageReading{}, musem.Wrap(err, musem.ENOTFOUND, "transcript %s does not exist", path)
		}
		return musem.UsageReading{}, musem.Wrap(err, musem.EUNAVAILABLE, "cannot read transcript %s", path)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return musem.UsageReading{}, musem.Wrap(err, musem.EUNAVAILABLE, "cannot stat transcript %s", path)
	}

	// Every path that starts the file over reports it, because the caller
	// accumulates: re-reading from the beginning without saying so adds the
	// file's history to a total that already holds it. The flag is what turns
	// "count these as well" into "count these instead".
	offset, remembered, usable := parseCursor(cursor)
	reset := !usable
	if cursor == CursorEnd {
		offset, reset = info.Size(), false
	}

	// A transcript that was deleted and written again is a different file that
	// happens to share a name. Size alone cannot tell it apart from one that
	// merely grew: replaced at or beyond the old offset, the read would resume
	// from the middle of a record it has never seen and silently skip
	// everything before it. The head of the file settles the question.
	current, err := fingerprint(f)
	if err != nil {
		return musem.UsageReading{}, musem.Wrap(err, musem.EUNAVAILABLE, "cannot read transcript %s", path)
	}
	// A cursor descended from CursorEnd carries a total that was never derived
	// from reading this file, so re-reading cannot reproduce it. Its lineage is
	// recorded in the cursor itself and every restart rule below defers to it:
	// discarding those totals would destroy exactly what CursorEnd was added to
	// protect.
	adopted := cursor == CursorEnd || remembered == CursorEnd

	switch {
	case adopted:
		// Deliberately unverifiable: this cursor exists to adopt a file whose
		// totals are already believed complete, so there is nothing to compare
		// against and nothing to re-read.

	case remembered != "" && current != "" && remembered != current:
		offset, reset = 0, true

	case remembered != "" && current == "":
		// The file was long enough to identify when the cursor was written and
		// is too short to identify now, so it cannot be the same file — it was
		// replaced by a smaller one. The size check below only catches that when
		// the replacement is shorter than the offset too; between the offset and
		// the fingerprint window it would otherwise resume into a stranger.
		offset, reset = 0, true

	case remembered == "" && current != "" && offset > 0:
		// The cursor was taken while the file was still too short to identify,
		// and the file can be identified now. Whether it is the same file is
		// unknowable, and resuming into a file this reader has never seen would
		// skip everything before the offset and add the rest to totals derived
		// from a different file.
		//
		// Starting over costs one re-read, once per file, and yields the right
		// total either way. It also retires cursors written before fingerprints
		// existed, which have the same unverifiability for the same reason.
		offset, reset = 0, true
	}

	// A file shorter than where we left off was replaced or truncated, so the
	// remembered offset is meaningless and the only safe move is to start over.
	if info.Size() < offset {
		offset, reset = 0, true
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return musem.UsageReading{}, musem.Wrap(err, musem.EUNAVAILABLE, "cannot seek transcript %s", path)
	}

	// The lineage outlives the file being too short to identify: without it the
	// next pass, once the head becomes hashable, would see a bare cursor and
	// start over.
	mark := current
	if mark == "" && adopted {
		mark = CursorEnd
	}

	usage, skipped, consumed, err := scanUsage(f)
	reading := musem.UsageReading{
		Entries: usage,
		Cursor:  formatCursor(mark, offset+consumed),
		Skipped: skipped,
		Reset:   reset,
	}
	if err != nil {
		return reading, err
	}
	return reading, nil
}

// CursorEnd resumes from wherever the transcript currently ends, counting
// nothing before it. It exists for accounting that was carried forward from
// somewhere with no cursor of its own, where the stored total is already
// believed complete and re-reading the file would add it to itself.
const CursorEnd = "end"

// fingerprintBytes is how much of a transcript's head is hashed to identify it.
// Enough to cover the first record, which carries the session's own start, and
// cheap enough to re-read on every pass.
const fingerprintBytes = 512

// fingerprint identifies a file by its head, so a replacement can be told from
// a continuation.
//
// A file shorter than the full head has no fingerprint, and that is deliberate
// rather than a gap. Hashing whatever happens to be there would produce a value
// that changes as the file grows — every append would look like a replacement
// and re-read the file from the start. Only a complete head is stable, because
// a transcript is append-only and its first bytes never change again.
//
// An empty result is therefore "not identifiable yet", not an error: a
// transcript that short has almost nothing counted against it either.
func fingerprint(f *os.File) (string, error) {
	buf := make([]byte, fingerprintBytes)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if n < fingerprintBytes {
		return "", nil
	}

	h := fnv.New64a()
	_, _ = h.Write(buf[:n])
	return strconv.FormatUint(h.Sum64(), 16), nil
}

// parseCursor turns a cursor back into a byte offset and the fingerprint of the
// file it was taken from, reporting whether it could be used at all.
//
// An empty cursor is usable: it means "from the start", and nothing has been
// counted yet. Anything else that cannot be read is not — the file still has to
// be read from the beginning, but the caller has totals from the cursor that
// was lost, and adding a full re-read to those would roughly double them.
//
// A bare number carries no fingerprint: either the file was too short to
// identify when it was written, or it predates fingerprints entirely. Either
// way the caller treats it as unverifiable — see ReadNew.
func parseCursor(cursor string) (offset int64, fingerprint string, usable bool) {
	if cursor == "" {
		return 0, "", true
	}

	if mark, rest, found := strings.Cut(cursor, cursorSeparator); found {
		offset, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || offset < 0 || mark == "" {
			return 0, "", false
		}
		return offset, mark, true
	}

	offset, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || offset < 0 {
		return 0, "", false
	}
	return offset, "", true
}

// cursorSeparator divides a cursor's file fingerprint from its byte offset.
const cursorSeparator = ":"

func formatCursor(fingerprint string, offset int64) string {
	at := strconv.FormatInt(offset, 10)
	if fingerprint == "" {
		return at
	}
	return fingerprint + cursorSeparator + at
}

// maxTranscriptLine bounds how much of a single record is held in memory.
// Transcript lines carry whole messages and are routinely far larger than a
// default scanner buffer; a line beyond even this is treated as corrupt rather
// than read, so a file that turns out to contain no newlines at all cannot
// exhaust memory.
const maxTranscriptLine = 8 << 20

// scanUsage reads usage entries from rd, returning how many bytes were consumed
// so the caller can resume exactly where parsing stopped.
//
// Only whole, newline-terminated lines are counted as consumed. musem reads
// transcripts while the agent is still appending to them, so a read landing
// mid-line is routine rather than exceptional: the trailing fragment is left
// where it is and picked up once the writer has finished it. Counting it as
// consumed would step over the completed record and lose its usage for good.
func scanUsage(rd io.Reader) (usage []musem.ModelUsage, skipped int, consumed int64, err error) {
	br := bufio.NewReaderSize(rd, 64*1024)

	for {
		line, n, oversize, readErr := readLine(br)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				// A fragment at the end is not a line yet, so its bytes stay
				// uncounted and the next read starts at the same place.
				return usage, skipped, consumed, nil
			}
			return usage, skipped, consumed, musem.Wrap(readErr, musem.EUNPARSEABLE, "reading transcript")
		}
		consumed += n

		if oversize {
			skipped++
			continue
		}
		// Trailing carriage returns are stripped along with the newline so a
		// transcript written with CRLF endings parses as JSON.
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			continue
		}

		var entry transcriptLine
		if err := json.Unmarshal(line, &entry); err != nil {
			skipped++
			continue
		}
		if entry.Type != "assistant" || entry.Message.Usage == nil {
			continue
		}
		if entry.Message.Model == "" {
			// Usage with no model attached cannot be priced, and attributing it
			// to a neighbouring model would produce a plausible wrong number.
			skipped++
			continue
		}

		u := entry.Message.Usage
		mu := musem.Usage{
			InputTokens:     u.InputTokens,
			OutputTokens:    u.OutputTokens,
			CacheReadTokens: u.CacheReadTokens,
		}

		switch {
		case u.CacheCreation != nil:
			mu.CacheWrite5mTokens = u.CacheCreation.Ephemeral5m
			mu.CacheWrite1hTokens = u.CacheCreation.Ephemeral1h

			// The split is trusted only as far as it accounts for the total the
			// same record reports. A tier this reader has never heard of would
			// otherwise vanish from both the token count and the bill, silently
			// and with nothing marked. Attributing the shortfall to the cheaper
			// tier keeps the tokens and understates the money, which is the same
			// trade the older-record case below makes.
			if short := u.CacheCreationTokens - (mu.CacheWrite5mTokens + mu.CacheWrite1hTokens); short > 0 {
				mu.CacheWrite5mTokens += short
			}
		default:
			// Older records carry only the total. Attribute it to the 5-minute
			// tier, which is the default time-to-live — the cheaper of the two,
			// so an unknown split understates rather than inflates the bill.
			mu.CacheWrite5mTokens = u.CacheCreationTokens
		}

		// A timestamp musem cannot read is left zero rather than guessed: the
		// consumer falls back to the current time, which is what it would have
		// used anyway, instead of a date invented here.
		var at time.Time
		if entry.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
				at = parsed
			}
		}

		usage = append(usage, musem.ModelUsage{Model: entry.Message.Model, Usage: mu, At: at})
	}
}

// readLine returns the next newline-terminated line together with its length on
// disk, including the newline. The returned slice is only valid until the next
// call, which is why every caller finishes with it before reading on.
//
// An oversize line is consumed and discarded rather than buffered: its bytes
// still have to be stepped over, but nothing requires holding them.
func readLine(br *bufio.Reader) (line []byte, n int64, oversize bool, err error) {
	for {
		chunk, err := br.ReadSlice('\n')
		n += int64(len(chunk))

		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			// The line spans more than one buffer, so it has to be copied out
			// before the next read overwrites it.
			if oversize || len(line)+len(chunk) > maxTranscriptLine {
				oversize = true
				line = nil
				continue
			}
			line = append(line, chunk...)
			continue

		case err != nil:
			// Includes io.EOF, which here means a trailing fragment with no
			// newline. The caller discards n along with it.
			return nil, n, oversize, err

		case oversize:
			return nil, n, true, nil

		case line == nil:
			// The whole line arrived in one piece; no copy needed.
			return chunk, n, false, nil

		default:
			return append(line, chunk...), n, false, nil
		}
	}
}
