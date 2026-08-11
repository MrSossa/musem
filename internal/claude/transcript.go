package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/MrSossa/musem"
)

// transcriptLine is the subset of a JSONL entry musem cares about. A transcript
// carries many record types; only assistant responses report usage.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
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

	offset := parseCursor(cursor)
	if cursor == CursorEnd {
		offset = info.Size()
	}
	// A file shorter than where we left off was replaced or truncated, so the
	// remembered offset is meaningless and the only safe move is to start over.
	if info.Size() < offset {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return musem.UsageReading{}, musem.Wrap(err, musem.EUNAVAILABLE, "cannot seek transcript %s", path)
	}

	usage, skipped, consumed, err := scanUsage(f)
	reading := musem.UsageReading{
		Entries: usage,
		Cursor:  formatCursor(offset + consumed),
		Skipped: skipped,
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

// parseCursor turns a cursor back into a byte offset. Anything unrecognised
// reads as "start from the beginning", which recounts rather than skips: an
// inflated total is visible and correctable, silently missing usage is not.
func parseCursor(cursor string) int64 {
	if cursor == "" {
		return 0
	}
	offset, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

func formatCursor(offset int64) string { return strconv.FormatInt(offset, 10) }

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
		default:
			// Older records carry only the total. Attribute it to the 5-minute
			// tier, which is the default time-to-live — the cheaper of the two,
			// so an unknown split understates rather than inflates the bill.
			mu.CacheWrite5mTokens = u.CacheCreationTokens
		}

		usage = append(usage, musem.ModelUsage{Model: entry.Message.Model, Usage: mu})
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
