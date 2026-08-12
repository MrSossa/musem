package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MrSossa/musem"
)

func TestShutdownError(t *testing.T) {
	// What bubbletea actually returns when its context is cancelled.
	killedByContext := fmt.Errorf("%w: %w", tea.ErrProgramKilled, context.Canceled)

	tests := []struct {
		name   string
		err    error
		ctxErr error
		want   error
	}{
		{
			name: "an orderly run",
		},
		{
			// SIGTERM from a supervisor: the process was asked to stop and did.
			name:   "killed after the signal context was cancelled",
			err:    killedByContext,
			ctxErr: context.Canceled,
		},
		{
			// Killed with no signal in sight is a real failure and must not be
			// swallowed along with the orderly case.
			name: "killed with the context still live",
			err:  killedByContext,
			want: killedByContext,
		},
		{
			name:   "an unrelated failure during shutdown",
			err:    musem.Errorf(musem.EUNAVAILABLE, "the terminal could not be opened"),
			ctxErr: context.Canceled,
			want:   musem.Errorf(musem.EUNAVAILABLE, "the terminal could not be opened"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shutdownError(tc.err, tc.ctxErr)
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("got %v, want a clean exit", got)
			case tc.want != nil && got == nil:
				t.Errorf("got a clean exit, want %v", tc.want)
			case tc.want != nil && !errors.Is(got, tea.ErrProgramKilled) && got.Error() != tc.want.Error():
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
