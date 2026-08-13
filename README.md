# musem

An observatory for the AI coding agent sessions running on your machine, and the
place you start them from. One screen answers "what is happening right now?" —
which session is working, which one has been waiting on you for ten minutes,
which one died, and what the whole thing is costing.

musem **observes**, and **launches**. It creates the worktrees it starts sessions
in, and takes them back when the session ends and there is nothing in them left
to lose. Everything else — sessions musem did not start, your repositories, the
worktrees you made yourself — it never touches.

```
  musem   3 sessions   $328.59 total

  STATUS         SESSION    BRANCH             DIRECTORY                     COST
▸ ◐ waiting      api        feat/rate-limit    ~/projects/api               $12.40
  ● running      web        main               ~/projects/web                $3.18
  ○ idle         docs       —                  ~/notes                       $0.92
```

## Running it

```sh
make build && ./bin/musem
```

Flags:

| Flag | Purpose |
| --- | --- |
| `--fake` | Serve fabricated sessions, for trying it out or developing the UI without live agents |
| `--interval` | How long to wait between refreshes (default 2s) |

Keys: `j`/`k` move, `g`/`G` jump to first/last, `enter` opens session detail,
`n` launches a session, `?` shows help, `q` quits.

Requires macOS or Linux. Native Windows is deliberately out of scope.
Launching additionally requires **tmux**, which hosts the session; without it
musem still observes everything on the machine and says what is missing when you
try to launch.

## Launching a session

Press `n`. A form opens with:

| Field | What it does |
| --- | --- |
| **Directory** | Where the session works. Filled in with the directory musem was started from, and editable. |
| **Worktree** | On by default. `space` toggles it. With it off the session starts in the directory as given and nothing is created. |
| **Branch** | With a worktree, a new branch is proposed. Edit the name, or press `ctrl-b` to pick one that already exists. |
| **Creates** | The path the worktree will occupy, derived from the repository and the branch, shown before anything is created. |

`tab` moves between fields, `enter` launches, `esc` cancels. Nothing at all
happens until you confirm: a form you abandon leaves no branch, no worktree and
no session behind.

The default is a worktree per session because two agents in one checkout fight
over the index and over each other's edits. That discipline is the whole reason
launching is worth automating, and it is the part people skip when it costs six
steps.

The session runs in tmux, detached, so it outlives musem: close the dashboard and
the agent keeps working. `tmux ls` lists them — everything musem started is
prefixed `musem-` — and `tmux attach -t <name>` gets you a terminal in it.

When a launch succeeds musem says what it started, and keeps saying it until the
session turns up in the inventory:

```
  ⟳ started musem-a1b2c3d4 — musem/session-1 in ~/projects/api-musem-session-1
    not in the inventory yet · tmux attach -t musem-a1b2c3d4
```

That line is normal for a few seconds: discovery runs on an interval. If it
stays, attach — the agent is almost certainly waiting for you. Claude Code asks
before it will work in a directory it has not been trusted with, and a fresh
worktree is by definition one of those. It puts that question in its pane and
waits there indefinitely; answer it once and the session joins the list.

Whether you see the question at all depends on where the worktree lands, because
trust is inherited from a parent directory: a repository under a path you have
already trusted produces worktrees that are trusted too. musem does not answer
that question for you, and will not write to the agent tool's configuration to
make it go away.

A launch that cannot proceed says so in the form rather than half-happening — the
directory is not a repository, the branch is already checked out somewhere else,
the destination is taken, tmux or the Claude CLI is missing. A launch that fails
after creating something undoes it, and if the undo itself fails it tells you
what is left on disk and where.

### When a session ends

musem takes back the worktree it created for that session, and only if the
worktree is clean. Clean means all four of:

- nothing uncommitted,
- nothing untracked,
- no commits the branch's remote has not seen — including the case where the
  branch has no remote at all, since then its commits exist in exactly one place,
- nothing stashed.

Anything else, or any of those four that git cannot answer, and the worktree
stays. The dashboard says which worktree survived and why. musem never removes a
worktree it did not create, however clean it is, and it never removes one you
have work in — the record of what it created is the precondition, and git's own
refusal to delete a dirty worktree is the second lock behind it.

## What it observes

Three things per session, each from the most reliable source available:

- **Status** — running, waiting on you, idle, or dead, from the agent tool's own
  query interface. When the available signals do not allow a confident answer,
  musem says *indeterminate* rather than guessing. A false "idle" is worse than
  an admitted gap: it makes you ignore a session that is waiting for you.
- **Cost** — from the `usage` the agent tool records for every response, priced
  per model. Cache tokens are accounted separately from ordinary input, and
  cache writes are split by time-to-live, because those are billed at different
  multiples of the input rate. A model with no known rate reports its tokens and
  marks the cost unavailable rather than applying a neighbour's price.
- **Branch** — resolved from the session's working directory. A directory
  outside a repository simply has no branch; that is normal, not an error.

Anything that goes stale is shown as stale. A source that is unavailable leaves
the last known data on screen with a reason, rather than an empty table or a
crash.

## Privacy

musem reads agent transcripts, which contain your source code and your prompts.
All processing is local and musem originates no network traffic — a property
asserted by a test (`internal/archtest`), not merely promised here. The test
walks the whole dependency graph, not just the imports musem writes itself, so
a dependency cannot do the reaching on its behalf. Musem's own code imports no
networking package at all.

Accumulated cost is kept in SQLite under your config directory, so history
survives both a restart and the transcripts being rotated away. How far each
transcript has been read is stored in the same row as the total it produced, so
a restart resumes where it left off rather than counting the same tokens again.

The same database records which worktrees musem created. That record is what
permits musem to remove one, so it has to outlive the process that made it —
ownership is never inferred from what a path looks like, because renaming a
directory would then either lose musem's own worktree or hand it somebody
else's. With no database, launching into a worktree is refused rather than done
and forgotten.

## Development

```sh
make test    # tests in the shipping configuration
make race    # tests under the race detector (needs cgo)
make lint    # golangci-lint
make run     # build and run
```

`CGO_ENABLED=0` is a project invariant: it is what keeps cross-compilation to
macOS and Linux possible from one machine, and CI fails if a build ends up
dynamically linked. The race detector is the one exception — it requires cgo, so
it runs as its own step. The invariant governs the shipped binary, not the test
runner.

`internal/archtest` asserts the architectural rules the layout only promises:
the root package depends on nothing outside the standard library, dependencies
point inward, adapters wrap exactly one foreign thing each, the UI fetches
nothing, and nothing reaches the network. Directory names do not enforce
boundaries; the direction of the imports does.

The UI is held to one rule beyond those: it cannot import `os` or `os/exec`, so
it cannot touch the disk itself. The launch form describes what should happen and
hands it to a launcher; that indirection is why a view with a write path behind
it cannot grow a second one by accident.

Planning artifacts live in `.ktools/`: open changes under `.ktools/changes/`,
living specs under `.ktools/specs/`.

## Workflow: GitHub flow

`main` is protected: **no direct pushes, no merges without a PR.**

1. Branch from `main`:

   ```bash
   git switch main && git pull
   git switch -c feat/my-change
   ```

2. Commit and push the branch:

   ```bash
   git push -u origin feat/my-change
   ```

3. Open the PR:

   ```bash
   gh pr create --fill
   ```

4. Before merging:
   - the branch must be up to date with `main` (`gh pr update-branch` or `git merge main`),
   - all PR conversations must be resolved.

5. Merge and clean up:

   ```bash
   gh pr merge --squash --delete-branch
   ```

### Rules active on `main`

- A pull request is required for any change.
- Conversation threads must be resolved.
- The branch must be up to date with `main`.
- Force-pushing and deleting `main` are forbidden.
