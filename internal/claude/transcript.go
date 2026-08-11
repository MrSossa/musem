package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sync"

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

// TranscriptReader reads usage from a session's JSONL transcript, remembering
// how far it got so subsequent reads only cover new lines.
//
// Transcripts are append-only, which is what makes incremental reading safe: a
// file that only grows can be resumed from a byte offset.
type TranscriptReader struct {
	mu      sync.Mutex
	offsets map[string]int64

	// warned records paths already reported as malformed, so a permanently
	// broken transcript produces one diagnostic rather than one per refresh.
	warned map[string]bool
}

// NewTranscriptReader returns a reader with no prior state.
func NewTranscriptReader() *TranscriptReader {
	return &TranscriptReader{
		offsets: make(map[string]int64),
		warned:  make(map[string]bool),
	}
}

// ReadNew returns the usage recorded in path since the last call for that path.
//
// Lines that cannot be understood are skipped rather than aborting the read: a
// single malformed record must not cost the caller every other figure in the
// file. The second return value reports how many lines were skipped, so the
// caller can surface that something was lost.
func (r *TranscriptReader) ReadNew(path string) (_ []musem.ModelUsage, skipped int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, musem.Wrap(err, musem.ENOTFOUND, "transcript %s does not exist", path)
		}
		return nil, 0, musem.Wrap(err, musem.EUNAVAILABLE, "cannot read transcript %s", path)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, musem.Wrap(err, musem.EUNAVAILABLE, "cannot stat transcript %s", path)
	}

	offset := r.offsets[path]
	// A file shorter than where we left off was replaced or truncated, so the
	// remembered offset is meaningless and the only safe move is to start over.
	if info.Size() < offset {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, musem.Wrap(err, musem.EUNAVAILABLE, "cannot seek transcript %s", path)
	}

	usage, skipped, consumed, err := scanUsage(f)
	if err != nil {
		return nil, skipped, err
	}
	r.offsets[path] = offset + consumed

	return usage, skipped, nil
}

// ShouldWarn reports whether a diagnostic for path has not been emitted yet,
// marking it as emitted. It exists so a permanently malformed transcript is
// reported once instead of on every refresh.
func (r *TranscriptReader) ShouldWarn(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warned[path] {
		return false
	}
	r.warned[path] = true
	return true
}

// scanUsage reads usage entries from rd, returning how many bytes were consumed
// so the caller can resume exactly where parsing stopped.
func scanUsage(rd io.Reader) (usage []musem.ModelUsage, skipped int, consumed int64, err error) {
	scanner := bufio.NewScanner(rd)
	// Transcript lines carry whole messages and can be far larger than the
	// default 64 KiB scanner limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		consumed += int64(len(line)) + 1 // +1 for the newline the scanner strips

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

	if err := scanner.Err(); err != nil {
		return usage, skipped, consumed, musem.Wrap(err, musem.EUNPARSEABLE, "reading transcript")
	}
	return usage, skipped, consumed, nil
}
