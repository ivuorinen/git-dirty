# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

## Project

`git-dirty` is a Go CLI and TUI that walks a directory for git working trees and
shows each one as a tree line: branch, dirty letters (`M S ? $`), ahead/behind
against the upstream, and the comparison with the default branch. It replaces a
bash script, kept as `scripts/git-dirty.sh` for `bench` and `compare.sh`, and
keeps its flags and `GIT_DIRTY_*` environment variables.

## Development Commands

```bash
mise install         # Go (go.mod toolchain line) and the pinned dev tools
mise run build       # Build ./git-dirty
mise run test        # Run the full test suite (needs git on PATH)
mise run test-race   # Tests with the race detector
mise run lint        # golangci-lint (.golangci.yml), no fixes: the CI check
mise run lint:fix    # golangci-lint, applying its fixes
mise run lint:all    # every pre-commit hook (prek) on every file
mise run hooks:install  # install the prek pre-commit hook
mise run vuln        # govulncheck
mise run all         # lint + test-race + vuln + build (the CI gates)
mise run bench       # hyperfine against the bash script
scripts/compare.sh   # per-repository equivalence with the bash script
go test ./internal/gitstat/ -run TestModified  # One test
go test ./internal/render/ -update  # Rewrite golden files; review the diff
```

## Architecture

`main.go` loads config, parses flags (`expandShortFlags` + an `fs.Parse` loop so
flags may follow the directory), then runs the TUI or plain mode (`useTUI`).

- **internal/config/**: defaults < `config.json` < `GIT_DIRTY_*` env. Flags
  are applied in `main`.
- **internal/git/**: stdlib-only readers for git's on-disk formats, SHA-1 and
  SHA-256. `repository.go` (git dir, commondir, format check), `config.go`,
  `refs.go` (loose, packed, symbolic; `ValidRefName`), `refspec.go`,
  `index.go` and `splitindex.go` (v2-v4, TREE, split index with EWAH),
  `odb.go`, `pack.go` and `delta.go` (loose objects, packs, deltas, a 16 MiB
  base cache), `objects.go` (commit, tree, tag), `ignore.go` (gitignore
  matcher), `worktree.go` (the linked worktrees a repository records, each
  checked to lead back to it).
- **internal/gitstat/**: per-repository inspection on top of internal/git.
  `Inspect` returns a `Result` and puts every failure in `Result.Err`,
  prefixed with its stage name. It imports internal/git as `gitrepo`, because
  its tests define a `git()` helper.
  - `head.go`: branch / exact tag / short hash.
  - `dirty.go`: `modified` (stat first, then hash), `staged` (cache-tree shortcut,
    else a tree walk), `untracked` (does not enter ignored dirs), `stashed`.
  - `ignore.go`: core.excludesFile resolution, `info/exclude` and the
    `.gitignore` stack.
  - `aheadbehind.go`: a priority-queue walk ordered by commit time, with a
    late-bit recount.
  - `refs.go`: upstream mapping through fetch refspecs, and the default branch.
- **internal/scan/**: discovery goroutine plus `GOMAXPROCS` workers. Every send
  selects on `ctx.Done()`. A repository's linked worktrees are queued as its
  children (`Item.Worktree`). A linked worktree the walk finds is held until
  the walk ends and sent only if no repository listed it, compared with
  `os.SameFile` because git records paths with symlinks resolved.
- **internal/tree/**: builds the display tree and applies the verbose, depth and
  dirty-only filters.
- **internal/render/**: tree lines become styled `Segment`s. Plain output writes
  ANSI codes; the TUI styles the same segments with lipgloss. `Sanitize` escapes
  control bytes in every string that comes from a repository.
- **internal/tui/**: bubbletea v2 model. Results arrive in 50 ms batches; a scan
  generation number drops results from a replaced scan.

## Rules that are easy to break

- **internal/git imports the standard library only.** No module may be added
  to read git data; depguard in `.golangci.yml` fails on any other import.
- **Everything internal/git reads may come from a hostile clone.** Keep the
  limits: `maxObjectSize` (256 MiB), `maxDeltaDepth` (10,000), delta copy
  bounds, `maxEWAHBits`, `symrefMaxDepth` (5), `maxTagDepth`,
  `maxTreeDepth` and `maxLinkFile` (4 KiB, regular files only, for `.git`
  and `worktrees/<id>/gitdir` files). Validate ref names with `ValidRefName`
  before they become paths, since `branch.*.merge` comes from repository
  config. A decoder returns an error and never panics; the fuzz targets in
  `fuzz_test.go` check that, and a new decoder gets a fuzz target.
- **Never execute `git` or anything else at runtime.** No hooks, no filters, no
  fsmonitor. Tests may call `git`, because they use it as the oracle;
  forbidigo rejects `exec.Command` outside `_test.go` files.
- **golangci-lint findings are fixed, not suppressed.** `//nolint:<linter> //
  <reason>` is only for code that is correct by design, such as `crypto/sha1`
  for git's object IDs or the tests' `exec.Command("git", ...)`; nolintlint
  checks every directive. A PostToolUse hook in `.claude/settings.json` runs
  `mise run lint:hook` (golangci-lint `--fix` on the edited file's package)
  after every `.go` edit and reports what is left.
- **The git CLI is the test oracle.** Tests build fixtures with `git` and
  compare against `git diff --quiet`, `git diff --cached --quiet`,
  `git ls-files --others --exclude-standard`, `git rev-list --left-right --count`,
  `git check-ignore`, `git ls-files --stage`, `git config --get`,
  `git show-ref`, `@{u}`, `git worktree list --porcelain` and
  `git rev-parse --git-dir`/`--git-common-dir`. Object reads are checked by
  re-hashing the content. Add a fixture there instead of a hand-written
  expected value. gitstat tables run in SHA-1 and SHA-256 repositories through
  `forEachFormat`.
- **Ahead/behind needs the late-bit recount.** Commits with equal timestamps,
  or clock skew, can deliver a tip's bit after a commit was counted;
  `walkState.add` uncounts and re-queues it. Mutating it away fails
  `TestAheadBehind`.
- **Global ignores come from `LoadExcludes`**, which reads both
  `~/.config/git/config` and `~/.gitconfig` and defaults to
  `~/.config/git/ignore`, as git does.
- **Fuzzing:** pass `-fuzzminimizetime=5s`; with the default 60 s, the fuzzer
  minimizing a new input looks like a hang.
- Golden files under `testdata/` are stored without trailing spaces, to satisfy
  `.editorconfig`. The TUI golden test trims the viewport's padding.
- `go.mod`'s `toolchain` line is the single Go version source, read by mise and
  by setup-go.
- **Commit types set the release version.** svu computes the next version from
  them, so type a commit by what it changes for users: a `feat:` or a `!`
  releases a new minor version. README "Releasing" lists which type releases
  what.

## Build & Release

- `.goreleaser.yaml`: gh-history's layout. Its before-hook generates
  `THIRD_PARTY_NOTICES.txt` with `go-licenses` v2.0.1 and `notices.tpl`, and the
  release attaches that file and `LICENSE`.
- Releases use Semantic Versioning tags (`v0.2.0`), the only version form
  `go install` understands. `.github/workflows/release.yml` runs on `v*.*.*`
  tags; `mise run release` tags the next version with svu. The first SemVer
  tag is made by hand (README "Releasing").
- `.github/workflows/ci.yml` runs the mise tasks on Linux and `go test` on
  macOS and Windows.
- `.github/workflows/codeql.yml` runs CodeQL for Go and Actions.
