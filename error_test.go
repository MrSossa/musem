package musem_test

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/MrSossa/musem"
)

func TestErrorfMessageAndCode(t *testing.T) {
	err := musem.Errorf(musem.EINVALID, "session %s is %d kinds of wrong", "abc", 2)

	if got, want := err.Code, musem.EINVALID; got != want {
		t.Errorf("Code = %q, want %q", got, want)
	}
	if got, want := err.Message, "session abc is 2 kinds of wrong"; got != want {
		t.Errorf("Message = %q, want %q", got, want)
	}
	if got, want := err.Error(), "musem: invalid: session abc is 2 kinds of wrong"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if err.Unwrap() != nil {
		t.Error("an error built by Errorf wraps nothing")
	}
}

func TestWrapPreservesTheCause(t *testing.T) {
	err := musem.Wrap(fs.ErrNotExist, musem.ENOTFOUND, "transcript %s is gone", "a.jsonl")

	if !errors.Is(err, fs.ErrNotExist) {
		t.Error("the wrapped cause must stay reachable through errors.Is")
	}
	if got, want := err.Error(), "musem: not_found: transcript a.jsonl is gone: file does not exist"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestErrorCodeWalksWrappedErrors is what lets the UI decide presentation from a
// code instead of matching on error text, however deep the adapter buried it.
func TestErrorCodeWalksWrappedErrors(t *testing.T) {
	deep := fmt.Errorf("outer: %w",
		fmt.Errorf("middle: %w", musem.Errorf(musem.EUNAVAILABLE, "the CLI is not installed")))

	if got, want := musem.ErrorCode(deep), musem.EUNAVAILABLE; got != want {
		t.Errorf("ErrorCode() = %q, want %q", got, want)
	}
	if got, want := musem.ErrorMessage(deep), "the CLI is not installed"; got != want {
		t.Errorf("ErrorMessage() = %q, want %q", got, want)
	}
}

func TestErrorCodeAndMessageForNil(t *testing.T) {
	if got := musem.ErrorCode(nil); got != "" {
		t.Errorf("ErrorCode(nil) = %q, want empty", got)
	}
	if got := musem.ErrorMessage(nil); got != "" {
		t.Errorf("ErrorMessage(nil) = %q, want empty", got)
	}
}

// TestForeignErrorsDoNotLeakInternals keeps an error musem did not classify from
// reaching the interface verbatim. It still gets a usable code, so no caller has
// to handle an empty one.
func TestForeignErrorsDoNotLeakInternals(t *testing.T) {
	foreign := errors.New("pq: password authentication failed for user \"admin\"")

	if got, want := musem.ErrorCode(foreign), musem.EINTERNAL; got != want {
		t.Errorf("ErrorCode() = %q, want %q", got, want)
	}
	if got, want := musem.ErrorMessage(foreign), "an internal error occurred"; got != want {
		t.Errorf("ErrorMessage() = %q, want %q", got, want)
	}
}
