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

	return r.transcripts.ReadNew(path, cursor)
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

	if path, ok := r.paths[sessionID]; ok {
		return path, nil
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

	delete(r.retry, sessionID)
	r.paths[sessionID] = matches[0]
	return matches[0], nil
}

func (r *UsageReader) retryAfter() time.Duration {
	if r.RetryLookupAfter > 0 {
		return r.RetryLookupAfter
	}
	return DefaultRetryLookup
}
