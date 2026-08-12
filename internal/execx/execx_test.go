package execx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stub writes an executable shell script and returns its path.
func stub(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stubs rely on a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "stub")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunReturnsOutput(t *testing.T) {
	bin := stub(t, "echo out\necho err >&2\nexit 0\n")

	res, err := Run(context.Background(), Cmd{Bin: bin, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if strings.TrimSpace(res.Stdout) != "out" || strings.TrimSpace(res.Stderr) != "err" {
		t.Errorf("stdout = %q, stderr = %q", res.Stdout, res.Stderr)
	}
}

// The kinds exist so callers can set policy without re-deriving the
// classification, and every caller here decides something different for each.
func TestKindsAreDistinguished(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		_, err := Run(context.Background(), Cmd{
			Bin:     filepath.Join(t.TempDir(), "nothing-here"),
			Timeout: 5 * time.Second,
		})
		assertKind(t, err, NotFound)
	})

	t.Run("exited", func(t *testing.T) {
		bin := stub(t, "exit 3\n")
		res, err := Run(context.Background(), Cmd{Bin: bin, Timeout: 5 * time.Second})
		assertKind(t, err, Exited)
		// The output is returned alongside the error, because a command that
		// failed may still have explained itself on the way out.
		if res.Stdout != "" {
			t.Errorf("stdout = %q, want empty", res.Stdout)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		bin := stub(t, "sleep 30\n")
		start := time.Now()
		_, err := Run(context.Background(), Cmd{Bin: bin, Timeout: 100 * time.Millisecond})
		assertKind(t, err, Timeout)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("took %s; the timeout did not bound the call", elapsed)
		}
	})
}

// A killed process also exits non-zero, so classifying by exit status alone
// would report a command musem never heard from as one that answered. Every
// caller treats those two oppositely: an exit is a routine answer, a timeout
// establishes nothing.
func TestATimedOutCommandIsNotReportedAsHavingExited(t *testing.T) {
	bin := stub(t, "sleep 30\n")

	_, err := Run(context.Background(), Cmd{Bin: bin, Timeout: 100 * time.Millisecond})
	assertKind(t, err, Timeout)
}

// The case the whole package exists for: the command answered and exited zero,
// and something it forked kept the pipe open past the delay. Reporting that as a
// failure throws away an answer that was captured in full, once per call, for as
// long as the command keeps leaving a process behind.
func TestAnAnswerSurvivesAChildThatOutlivesTheCommand(t *testing.T) {
	bin := stub(t, "echo answer\nsleep 30 &\nexit 0\n")

	res, err := Run(context.Background(), Cmd{
		Bin:      bin,
		Timeout:  30 * time.Second,
		Answered: func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("err = %v, want nil: the command answered and only its child outlived it", err)
	}
	if strings.TrimSpace(res.Stdout) != "answer" {
		t.Errorf("stdout = %q, want the answer that was already captured", res.Stdout)
	}
}

// Answered is the caller's because the evidence differs. A caller that needs a
// complete answer rejects a fragment, and gets an error rather than a truncated
// value it would go on to believe.
func TestACallerCanRefuseAnIncompleteAnswer(t *testing.T) {
	bin := stub(t, "printf fragment\nsleep 30 &\nexit 0\n")

	_, err := Run(context.Background(), Cmd{
		Bin:      bin,
		Timeout:  30 * time.Second,
		Answered: func(out string) bool { return strings.HasSuffix(out, "\n") },
	})
	if err == nil {
		t.Fatal("a fragment must not be accepted as a complete answer")
	}
}

// A nil predicate means never, so a caller that did not think about it gets the
// error rather than a silent acceptance of whatever happened to arrive.
func TestWithoutAPredicateAWaitDelayFailureStaysAFailure(t *testing.T) {
	bin := stub(t, "echo answer\nsleep 30 &\nexit 0\n")

	if _, err := Run(context.Background(), Cmd{Bin: bin, Timeout: 30 * time.Second}); err == nil {
		t.Fatal("want an error when no predicate says the command answered")
	}
}

// Every caller is on a refresh loop, so an unbounded call is a wedged interface.
// Refusing is better than defaulting: a default would silently pick a number the
// caller never chose.
func TestATimeoutIsRequired(t *testing.T) {
	_, err := Run(context.Background(), Cmd{Bin: "true"})
	assertKind(t, err, Failed)
}

func assertKind(t *testing.T, err error, want Kind) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want kind %d", want)
	}
	var xerr *Error
	if !errors.As(err, &xerr) {
		t.Fatalf("err = %v (%T), want an *execx.Error", err, err)
	}
	if xerr.Kind != want {
		t.Errorf("kind = %d, want %d (err: %v)", xerr.Kind, want, err)
	}
}
