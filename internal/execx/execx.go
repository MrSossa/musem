// Package execx runs an external command with a bounded lifetime, and reports
// back in the few shapes a caller actually needs to tell apart.
//
// It is not an adapter. Adapters are named after the foreign system they wrap
// and hold musem's knowledge of that system's format; this package wraps
// os/exec, which is the standard library, and knows nothing about any command it
// runs. It exists because the awkward part of shelling out — a child that
// outlives the process that was waited on — is subtle enough to get wrong once
// and far too subtle to get right twice independently. It therefore imports
// nothing from musem and stays a leaf, which is asserted by a test.
package execx

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"time"
)

// waitDelay is how long Run keeps waiting for output plumbing after the process
// itself has been killed.
//
// Killing the process is not the same as getting the call back. Run waits for
// the goroutine copying into its buffers, and that goroutine finishes only when
// every process holding the pipe's write end has exited — the context kills the
// direct child, not a grandchild it left behind. Without a delay a credential
// helper, a hook, or anything wedged on a stalled mount holds the call open past
// the timeout and freezes the refresh loop the timeout exists to protect.
const waitDelay = time.Second

// Kind classifies why a command did not answer.
type Kind int

const (
	// NotFound means the binary is not there: either PATH could not resolve a
	// bare name, or a full path does not exist.
	NotFound Kind = iota
	// Timeout means the command was killed for exceeding its budget, so it
	// established nothing about what it was asked.
	Timeout
	// Exited means the command ran and returned non-zero. Whether that is a
	// failure or a routine answer is the caller's to decide: `git rev-parse` in
	// a directory that is not a repository exits non-zero and is not a problem.
	Exited
	// Failed is anything else: a pipe that broke, a signal, a plumbing error.
	Failed
)

// Error is what Run returns when a command did not answer cleanly. The
// underlying error is preserved so a caller can wrap it and keep the cause
// chain intact.
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Cmd is one command to run.
type Cmd struct {
	// Bin is the executable. It is first-party configuration, never a value
	// that arrived from outside musem.
	Bin string

	// Args are passed as an argv, so no shell reinterprets them.
	Args []string

	// Timeout bounds the call. Zero means the caller forgot; Run rejects it
	// rather than waiting forever, because every caller here is on a refresh
	// loop that must not be allowed to stall.
	Timeout time.Duration

	// Answered decides whether a WaitDelay-only failure should count as
	// success, given what landed on stdout. See Run.
	//
	// It is the caller's because the evidence differs: a command whose output
	// is self-delimiting can accept whatever arrived and let its own parser
	// complain, while one that returns a bare word needs the terminating
	// newline to tell a complete answer from a fragment. Nil means never.
	Answered func(stdout string) bool
}

// Result is what the command wrote.
type Result struct {
	Stdout string
	Stderr string
}

// Run executes c and returns what it wrote.
//
// A non-nil error is always an *Error, so a caller can switch on Kind and
// decide policy without re-deriving the classification.
func Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Timeout <= 0 {
		return Result{}, &Error{Kind: Failed, Err: errors.New("execx: a timeout is required")}
	}

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	// The binary is a variable, which is what gosec objects to, and nothing an
	// attacker reaches: every caller passes a field of its own defaulted
	// configuration. The arguments beside it go as an argv — there is no shell —
	// so a value carrying spaces or metacharacters is one argument, not a second
	// command. Resolving via PATH is the intent: musem runs the binaries the
	// user's own shell would.
	// #nosec G204 -- Bin is first-party configuration; argv, no shell
	cmd := exec.CommandContext(ctx, c.Bin, c.Args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = waitDelay

	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return res, nil
	}

	// WaitDelay closes the pipes when the process holding their write end is not
	// the one that was waited on — a hook, a helper, anything the command forks
	// and detaches — and Run then reports ErrWaitDelay for a command that exited
	// zero. Taking that as a failure throws away an answer that was captured in
	// full, once per call, for as long as the command keeps leaving a process
	// behind.
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() &&
		c.Answered != nil && c.Answered(res.Stdout) {
		return res, nil
	}

	// Two shapes for the same fact. A bare name that PATH cannot resolve fails
	// in LookPath as *exec.Error; a path that does not exist fails in the exec
	// itself as *fs.PathError. Matching only the first reports a missing binary
	// as a generic failure whenever the caller was configured with a full path,
	// which is exactly how musem is wired in a test and how a user would point
	// it at a CLI installed somewhere unusual.
	var notFound *exec.Error
	if errors.As(err, &notFound) || errors.Is(err, fs.ErrNotExist) {
		return res, &Error{Kind: NotFound, Err: err}
	}
	// Checked before ExitError: a killed process also exits non-zero, and
	// reading that as the command's own answer would turn "we never found out"
	// into a claim.
	if ctx.Err() != nil {
		return res, &Error{Kind: Timeout, Err: err}
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		return res, &Error{Kind: Exited, Err: err}
	}
	return res, &Error{Kind: Failed, Err: err}
}

// KindOf classifies why err was returned by Run.
//
// Run guarantees a non-nil error is always an *Error, so this is the unwrap that
// every caller would otherwise write for itself — and did, in each adapter that
// shells out, each one re-deriving a classification this package had already
// attached. Anything unrecognised, including nil, is Failed: callers reach for
// the kind only inside a branch they took because there was an error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return Failed
}
