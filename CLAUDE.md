# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

## Project

`git-dirty` is a Go CLI and TUI that walks a directory for git working trees and
shows each one as a tree line: branch, dirty letters (`M S ? $`), ahead/behind
against the upstream, and the comparison with the default branch. A bare
repository is a 🪵 line with its worktrees under it. The TUI can fetch and
pull the selected repository. It replaces a
bash script, kept as `scripts/git-dirty.sh` for `bench` and `compare.sh`, and
keeps its flags and `GIT_DIRTY_*` environment variables.

## Development Commands

```bash
mise install         # Go (go.mod toolchain line), the newest git and the pinned dev tools
mise run build       # Build ./git-dirty
mise run test        # Run the full test suite with the newest git (mise.toml)
mise run test-race   # Tests with the race detector
mise run test-oldest-git  # Tests with the oldest supported git (2.39.2)
mise run lint        # golangci-lint (.golangci.yml), no fixes: the CI check
mise run lint:fix    # golangci-lint, applying its fixes
mise run lint:all    # every pre-commit hook (prek) on every file
mise run hooks:install  # install the prek pre-commit hook
mise run vuln        # govulncheck
mise run all         # lint + test-race + test-oldest-git + vuln + build (the CI gates)
mise run bench       # hyperfine against the bash script
scripts/compare.sh   # per-repository equivalence with the bash script
go test ./internal/gitstat/ -run TestModified  # One test
go test ./internal/render/ -update  # Rewrite golden files; review the diff
```

## Architecture

`main.go` parses the flags once over the defaults to find `-config`, loads
that file, parses again over it (`expandShortFlags` + an `fs.Parse` loop so
flags may follow the directory), then runs the TUI or plain mode (`useTUI`).
`session.tuiConfig` builds the TUI's settings from the file layer, at startup
and again for each settings-page save.

- **internal/config/**: defaults < `config.json` < `GIT_DIRTY_*` env. Flags
  are applied in `main`. `LoadFile` is the file layer, `ApplyEnv` adds the
  environment, `Load` is both. `Save` writes only keys that differ from
  `Defaults()`, through a temporary file renamed over the target (a
  symlinked file's target), keeping its mode.
- **internal/git/**: stdlib-only readers for git's on-disk formats, SHA-1 and
  SHA-256. `repository.go` (git dir, commondir; `IsGitDir`, git's test for a
  folder that is itself a git dir; `Repository.Bare`, set for such a folder or
  for `core.bare` without commondir), `format.go` (repository format version,
  `knownExtensions`), `hash.go` (object IDs, object hashing), `config.go`,
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
  selects on `ctx.Done()`. A folder holding `.git`, or passing
  `gitrepo.IsGitDir` (a bare repository), is a repository and is not entered;
  `gitstat.Inspect` stops at the open stage for a bare one (`Result.Bare`).
  A repository's linked worktrees are queued as its children
  (`Item.Worktree`), and each is claimed, even one listed with an error,
  except one whose `.git` file leads to another repository
  (`gitrepo.ErrOtherGitDir`). A linked worktree the walk finds, or a folder
  whose worktrees cannot be listed (a worktree with a broken `.git` file is
  one), is held until the walk ends and sent only if no repository claimed it,
  compared with `os.SameFile` because git records paths with symlinks
  resolved.
- **internal/tree/**: builds the display tree and applies the verbose, depth and
  dirty-only filters. `Node.Problem` (a dirty or unreadable repository or
  worktree below) is the dirty-only rule and also picks a directory's icon:
  📁, or ⚠️ with a problem below.
- **internal/render/**: tree lines become styled `Segment`s. Plain output writes
  ANSI codes; the TUI styles the same segments with lipgloss. `Sanitize` escapes
  control bytes in every string that comes from a repository.
- **internal/tui/**: bubbletea v2 model. Results arrive in 50 ms batches; a scan
  generation number drops results from a replaced scan. A cursor selects a
  line, kept by `Node.Path` across redraws. `actions.go`: `f`/`F`/`p`/`P`
  on the selected repository, one at a time, never during a scan, in a
  goroutine `stop` waits for, with a generation number of their own; a
  rescan follows each. `mass.go`: `m`, the same actions on every listed
  repository `Check` accepts (`massTargets`; a fetch skips worktrees listed
  under their repository), one at a time, with a progress grid.
  `settings.go`: `s`, a form over the config file's layer (no `interactive`
  row) with a line editor of its own: `bubbles/textinput` imports
  `atotto/clipboard`, which runs clipboard programs on paste.
- **internal/action/**: the TUI's fetch and pull. `Kind` (`Fetch`,
  `FetchPrune`, `Pull`, `PullRebase`), `Check` (which repositories an action
  runs on, from the `gitstat.Result`) and `Run`, the one place git-dirty runs
  a program: `git` with fixed arguments in the repository's directory,
  without git's repository-local environment variables, with no terminal
  (`proc_unix.go`: `Setsid`, SIGINT to the process group on cancel, and
  `reap`'s SIGKILL to what is left of it once git has exited;
  `proc_windows.go`: `CREATE_NO_WINDOW`), keeping the last 4 KiB of output.

## Rules that are easy to break

- **internal/git imports the standard library only.** No module may be added
  to read git data; depguard in `.golangci.yml` fails on any other import.
- **Everything internal/git reads may come from a hostile clone.** Keep the
  limits: `maxObjectSize` (256 MiB), `maxDeltaDepth` (10,000), delta copy
  bounds, `maxEWAHBits`, `symrefMaxDepth` (5), `maxTagDepth`,
  `maxTreeDepth`, `maxLinkFile` (4 KiB, regular files only, for `.git`,
  `worktrees/<id>/gitdir` and `commondir` files), `maxConfigFile` (16 MiB,
  `ReadConfigFile` through `OpenRegular`, so a symlink to a device fails)
  and `maxHeadRef` (255 bytes of `HEAD` in `IsGitDir`, whose symlinked
  `HEAD` is judged by its target, never followed). `IsGitDir` makes `Open`
  reachable from checked-out content, not only from git-created `.git`
  dirs. Validate ref names with `ValidRefName`
  before they become paths, since `branch.*.merge` comes from repository
  config. A decoder returns an error and never panics; the fuzz targets in
  `fuzz_test.go` check that, and a new decoder gets a fuzz target.
- **Only internal/action runs a program, and only `git`.** It runs on a key
  press in the TUI, with a fixed argv that holds nothing taken from a
  repository (the repository is `cmd.Dir`, for a bare one too, so git
  applies the user's `safe.bareRepository`), git's repository-local
  environment variables stripped (`localEnv`, checked against
  `git rev-parse --local-env-vars`), `GIT_TERMINAL_PROMPT=0` and no
  terminal. Nothing else executes anything at runtime: the scan runs no
  hooks, no filters, no fsmonitor. Tests may call `git`, because they use it
  as the oracle; forbidigo rejects `exec.Command` outside `_test.go` files,
  and `internal/action/run.go` holds the only `//nolint:forbidigo`.
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
  `git rev-parse --git-dir`/`--git-common-dir`/`--is-inside-work-tree`.
  Object reads are checked by
  re-hashing the content. Add a fixture there instead of a hand-written
  expected value. gitstat tables run in SHA-1 and SHA-256 repositories through
  `forEachFormat`. Tests must pass with the newest and the oldest supported
  git, both pinned in `mise.toml` (`test`/`test-race` and `test-oldest-git`)
  for Linux and macOS; Windows uses the git on `PATH`.
  git rewords its messages between releases (2.56 reworded the error for a
  `.git` file naming a missing directory), so compare exit codes and output,
  not an error's wording, or accept each release's wording. A test that needs
  a newer git skips itself on an older one, as
  `TestListWorktrees_RelativePaths` does without `--relative-paths`.
- **Ahead/behind needs the late-bit recount.** Commits with equal timestamps,
  or clock skew, can deliver a tip's bit after a commit was counted;
  `walkState.add` uncounts and re-queues it. Mutating it away fails
  `TestAheadBehind`.
- **Global ignores come from `LoadExcludes`**, which reads the system config
  (unless `GIT_CONFIG_NOSYSTEM`), then `GIT_CONFIG_GLOBAL` or else
  `$XDG_CONFIG_HOME/git/config` and `~/.gitconfig`, and defaults to
  `$XDG_CONFIG_HOME/git/ignore`, as git does.
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
