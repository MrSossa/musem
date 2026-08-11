package claude

import (
	"context"
	"os"
	"path/filepath"
	"sync"

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

	transcripts *TranscriptReader

	mu    sync.Mutex
	paths map[string]string // sessionID -> resolved transcript path
}

// NewUsageReader returns a reader over the default transcript location.
func NewUsageReader() *UsageReader {
	return &UsageReader{
		transcripts: NewTranscriptReader(),
		paths:       make(map[string]string),
	}
}

// ReadUsage returns usage recorded for a session since the previous call.
func (r *UsageReader) ReadUsage(_ context.Context, sessionID string) ([]musem.ModelUsage, error) {
	path, err := r.resolve(sessionID)
	if err != nil {
		return nil, err
	}

	entries, _, err := r.transcripts.ReadNew(path)
	if err != nil {
		return nil, err
	}

	out := make([]musem.ModelUsage, 0, len(entries))
	for _, e := range entries {
		out = append(out, musem.ModelUsage{Model: e.Model, Usage: e.Usage})
	}
	return out, nil
}

// resolve finds the transcript for a session, remembering the answer so the
// search happens once per session rather than on every refresh.
func (r *UsageReader) resolve(sessionID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if path, ok := r.paths[sessionID]; ok {
		return path, nil
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
		return "", musem.Errorf(musem.ENOTFOUND, "no transcript found for session %s", sessionID)
	}

	r.paths[sessionID] = matches[0]
	return matches[0], nil
}
