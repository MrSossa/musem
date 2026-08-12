# musem

A read-only observatory for the AI coding agent sessions running on your
machine. One screen answers "what is happening right now?" — which session is
working, which one has been waiting on you for ten minutes, which one died, and
what the whole thing is costing.

musem **observes**. It does not start sessions, stop them, send them input, or
touch your repositories.

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
`?` shows help, `q` quits.

Requires macOS or Linux. Native Windows is deliberately out of scope.

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
