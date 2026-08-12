# musem — project context

musem is a personal terminal-based session manager for AI coding agents. The
unit of work is the session: each one has its own status, branch, worktree and
cost.

## Tech stack

- Go. Single static binary, `CGO_ENABLED=0` always (cross-compilation depends on
  it). Persistence via `modernc.org/sqlite`, never `mattn/go-sqlite3`.
- TUI with Bubble Tea + Lipgloss + Bubbles. Hard rule: every event source lives
  in its own goroutine and pushes typed messages through `Program.Send`; nobody
  touches the model outside `Update`.
- tmux as the session substrate (it provides PTY handling, resize, scrollback and
  persistence). Git by shelling out to the binary, not `go-git`.
- Config paths via `os.UserConfigDir()`.

## Package layout

The root package holds the domain (`musem.Session`, `musem.Status`) plus
`error.go` with application error codes, and imports nothing outside the standard
library. Everything else lives under `internal/`: adapters are named after what
they wrap (`claude`, `sqlite`, `git`, `inmem`), orchestration gets capability
packages (`registry`, `cost`), `app` composes them and `tui` renders.

Consumers declare the interfaces they need — no ports package. Dependencies point
inward, `main.go` is the only place that wires the graph, and there is no
dependency container passed around wholesale. Import direction is asserted by a
test (`internal/archtest`), not by discipline.

## Platforms

macOS and Linux. Native Windows is explicitly out of scope — that renunciation is
what makes depending on tmux possible.

## Domain: how an agent is observed

Three lanes, and the order matters:

1. **Structured channels** (preferred): the JSONL transcripts under
   `~/.claude/projects/<escaped-cwd>/<sessionId>.jsonl` carry real usage per
   message; `claude agents --json` reports pid, cwd, sessionId and status; hooks
   (Stop, Notification, PreToolUse...) push events.
2. **Process signals**: alive/dead, exit code.
3. **Scraping the pane**: last resort. It is a presentation layer and rots with
   every UI change in the tool.

Never derive from rendered pane text something that exists as structured data.

## Conventions

- GitHub flow. `main` is protected: branch + PR, no direct push.
- Everything in English: specs, docs, identifiers, code and commits.

## Spec-driven development

Planning artifacts live in `.ktools/`: open changes under `.ktools/changes/`,
living specs under `.ktools/specs/`, archived changes under
`.ktools/changes/archive/`. The workflow is propose → implement → review →
archive; see the `ktools:sdd-workflow` skill for the layout, templates and phase
rules.
