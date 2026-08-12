package claude

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MrSossa/musem"
)

// UsageReader resolves a session's transcript and reports the usage recorded in
// it since the last read.
//
// The transcript lives under a directory named after the session's working
// directory, which musem does not necessarily know — so the file is located by
// searching for the session identifier rather than by reconstructing the path
// encoding, which is a foreign convention that could change.
type UsageReader struct {
	// ProjectsDir is the root holding one directory per project. Empty means
	// the default location under the user's home directory.
	ProjectsDir string

	// RetryLookupAfter is how long a failed search is remembered before it is
	// attempted again. Zero means DefaultRetryLookup.
	RetryLookupAfter time.Duration

	// transcripts is a value, not a pointer: the reader holds no state, so the
	// zero value works and there is nothing to construct or to synchronise.
	transcripts TranscriptReader
	now         func() time.Time

	mu    sync.Mutex
	paths map[string]string    // sessionID -> resolved transcript path
	retry map[string]time.Time // sessionID -> when to search again after a miss
}

// DefaultRetryLookup is how long a session with no transcript is left alone
// before musem looks for it again.
const DefaultRetryLookup = 30 * time.Second

// NewUsageReader returns a reader over the default transcript location.
func NewUsageReader() *UsageReader {
	return &UsageReader{
		now:   time.Now,
		paths: make(map[string]string),
		retry: make(map[string]time.Time),
	}
}

// init prepares a reader built as a composite literal, so setting only the
// exported fields is enough to get a working one.
func (r *UsageReader) init() {
	if r.now == nil {
		r.now = time.Now
	}
	if r.paths == nil {
		r.paths = make(map[string]string)
	}
	if r.retry == nil {
		r.retry = make(map[string]time.Time)
	}
}

// ReadUsage returns usage recorded for a session since cursor.
func (r *UsageReader) ReadUsage(_ context.Context, sessionID, cursor string) (musem.UsageReading, error) {
	path, err := r.resolve(sessionID)
	if err != nil {
		return musem.UsageReading{}, err
	}

	reading, err := r.transcripts.ReadNew(path, cursor)
	if musem.ErrorCode(err) == musem.ENOTFOUND {
		// The path resolved once but no longer exists: the transcript was
		// deleted, or the session's directory was renamed, which moves the file
		// because the tool encodes the working directory into its location.
		// Forgetting the answer sends the next pass back to the search, where a
		// genuine disappearance settles into the retry backoff. Keeping it would
		// freeze the session's cost for the rest of the process.
		r.forget(sessionID)
	}
	return reading, err
}

// forget drops a resolved path so the next read searches for it again.
func (r *UsageReader) forget(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.paths, sessionID)
}

// resolve finds the transcript for a session, remembering the answer so the
// search happens once per session rather than on every refresh.
//
// A failed search is remembered too, for a while. Sessions are never dropped
// from the registry, so without this a session whose transcript was deleted
// would re-scan every project directory on every refresh, forever.
func (r *UsageReader) resolve(sessionID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()

	if known, cached := r.paths[sessionID]; cached {
		// Kept for as long as the file is there, and deliberately not re-chosen
		// against the other candidates.
		//
		// Two transcripts can carry one session identifier — a renamed working
		// directory leaves the old file on disk — and picking between them by
		// modification time oscillates: anything that touches the loser (a
		// backup, an editor, a copy) makes it the winner again. Every switch
		// reads a file this reader has not seen from byte zero, which reports a
		// restart, which wipes the session's accumulated total and its stored
		// history. A figure that stops moving is recoverable; one that is
		// destroyed on every flip is not.
		if _, err := os.Stat(known); err == nil {
			return known, nil
		}
		delete(r.paths, sessionID)
	}

	if until, ok := r.retry[sessionID]; ok && r.now().Before(until) {
		return "", musem.Errorf(musem.ENOTFOUND, "no transcript found for session %s", sessionID)
	}

	root := r.ProjectsDir
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", musem.Wrap(err, musem.EUNAVAILABLE, "cannot resolve the home directory")
		}
		root = filepath.Join(home, ".claude", "projects")
	}

	matches, err := filepath.Glob(filepath.Join(root, "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		// Remembered rather than cached permanently: a session can be seen
		// before its transcript is written, and giving up on it for good would
		// leave it uncounted for the rest of the run.
		r.retry[sessionID] = r.now().Add(r.retryAfter())
		return "", musem.Errorf(musem.ENOTFOUND, "no transcript found for session %s", sessionID)
	}

	// Choosing happens once per session, so modification time is safe here in a
	// way it is not on re-resolution: the live transcript is the one last
	// written to, and taking the first match instead would pick by name.
	best := newest(matches)
	delete(r.retry, sessionID)
	r.paths[sessionID] = best
	return best, nil
}

// newest returns the most recently modified match.
func newest(matches []string) string {
	best, bestAt := matches[0], time.Time{}
	for _, candidate := range matches {
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.ModTime().After(bestAt) {
			best, bestAt = candidate, info.ModTime()
		}
	}
	return best
}

func (r *UsageReader) retryAfter() time.Duration {
	if r.RetryLookupAfter > 0 {
		return r.RetryLookupAfter
	}
	return DefaultRetryLookup
}
