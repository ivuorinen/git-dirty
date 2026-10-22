# Plan: git-dirty — Go CLI + TUI

Date: 2026-10-22
Status: DRAFT — awaiting approval to implement

## Goal

Replace `~/.local/bin/git-dirty` (1113-line bash) with a single static Go binary
that walks a directory, prints the git repositories it finds as a tree, and for
each repository shows its current branch, its dirty state, and how far it is
ahead of or behind its upstream. When the checked-out branch is not the default
branch, it also shows (a) how far the local default branch is ahead of or behind
its own upstream and (b) how far the current branch has diverged from the
default branch. Interactive TUI by default on a terminal, plain output otherwise.

## Decisions taken (user, 2026-10-22)

| Question | Answer |
| --- | --- |
| Default-branch comparison | **Both**: local default vs its upstream, and current vs default |
| TUI invocation | **TUI by default on a TTY**; plain output when piped or with `--plain` |
| Config file | **New JSON file** (`encoding/json`); the old shell `config` is not read |
| Dirty engine | **Custom code on go-git plumbing**, not `Worktree.Status()` |

## Scope & constraints

- Greenfield repo (`main`, no commits). Module `github.com/ivuorinen/git-dirty`.
- Go: `go 1.27.3` in `go.mod`. That is the latest release (go.dev/VERSION,
  2026-10-19), and `go.mod` is the only place the version lives. It is the
  same layout as gh-history: mise reads it via
  `idiomatic_version_file_enable_tools`, and setup-go reads it via
  `go-version-file`.
- Dependencies are limited to:
  - `github.com/go-git/go-git/v5` v5.19.3. v6 is still `v6.0.0-beta.1`.
  - `charm.land/bubbletea/v2` v2.1.0, `charm.land/bubbles/v2` v2.2.1 and
    `charm.land/lipgloss/v2` v2.0.6, for the TUI only.

  Everything else comes from the stdlib: `flag`, `encoding/json`, `os`,
  `io/fs`, `container/heap`, `context`, `os/signal` and `sync`. Plain mode
  renders with stdlib only.
- **Only go-git packages below the root are imported**: `storage/filesystem`,
  `storage/filesystem/dotgit`, `plumbing/...` and `config`. Importing the root
  `git` package links `plumbing/transport` (HTTP/SSH clients) into the
  binary. Verified: `config` and `storage/filesystem` do not import transport.
- Release tooling mirrors gh-history:
  - `.goreleaser.yaml`: version 2, `CGO_ENABLED=0`, `-trimpath`, `-s -w`,
    linux/darwin/windows × amd64/arm64 minus windows/arm64, `formats: [binary]`,
    cosign-signed checksums and `extra_files`.
  - `mise.toml` tasks, the `ci.yml`, `release.yml` and `codeql.yml`
    workflows, and CalVer `20*` tags.
- Read-only and offline:
  - No git writes and no fetch.
  - Ahead/behind is measured against the remote-tracking refs as of the last
    fetch.
  - No external process is ever executed. That includes `git`, hooks,
    `core.fsmonitor` and filters.
- Must not change: the bash script stays installed until Task 18 passes.

### Behavior carried over from the bash script

| Script | Go binary |
| --- | --- |
| `GIT_DIRTY_DIR` (default `~/Code`), positional `[directory]` | same |
| Exclude by basename `node_modules vendor .cache build dist .tests .test`, `-e a,b` appends | same |
| Skip a dir containing `.ignore` | same |
| `GIT_DIRTY_MAXDEPTH` / `-m` (15) recursion limit | same |
| Stop descending at a dir with a `.git` entry (dir or `gitdir:` file) | same |
| Symlinked dirs not followed | same |
| Non-git dirs shown only when they contain repos; `-v` shows all | same |
| `-d` / `GIT_DIRTY_DEPTH`: declared, **read nowhere** (only in help text) | applied: in `-v` mode, empty non-git dirs deeper than N are not listed |
| `-t` truncation: effectively a no-op (each line prints a basename either way) | `-t` off prints each repo line with its full path |
| `-c` / `-b` / `-t` toggles, `-a` (stash + untracked), `-v` | same |
| `-p` parallel (needs GNU parallel) | accepted and ignored; work is always concurrent |
| Status letters `M S ? $ ↑` | `M S ? $` in brackets; `↑N ↓N` become explicit counts |
| Repo is ❌ when any letter is set, including unpushed `↑` | same: ❌ when M/S/?/$ is set or ahead > 0; behind does not make it dirty |
| Branch name hidden for `GIT_DIRTY_MAIN_BRANCHES` (`main master trunk`) | same; the counts are still shown |
| Branch fallback: symbolic-ref → exact tag → short hash | same |
| Bundled flags (`-vac`, `-d3`) via getopts | supported by a small pre-expansion step before `flag.Parse` |
| Config: shell-sourced `~/.config/git-dirty/config` | `~/.config/git-dirty/config.json` (no file exists on this machine today) |

### Output format (plain mode, color off)

```text
Checking repositories in: /home/u/Code
Legend: ✅ Clean repo | ❌ Dirty repo | ⚠️ Directory / error
---------------------------------------------------------
⚠️ /home/u/Code
├── ⚠️ ivuorinen
│   ├── ❌ git-dirty [M?] (feature/tree ↑2) · main ↓4 · vs main ↑5 ↓1
│   ├── ✅ dotfiles ↓3
│   ├── ❌ gh-history ↑1
│   └── ⚠️ broken ! index: unknown extension (split index)
└── ✅ scratch (wip, no upstream) · vs main ↑0 ↓0

Found 5 git repositories in 0.21s
```

Line format, in order: `<tree prefix><icon> <name>`, then `[<letters>]`,
`<branch segment>`, `[· <default> ↑x ↓y]` and `[· vs <default> ↑p ↓q]`.

- **Letters** `[MS?$]` appear only when non-empty.
- **Branch segment**:
  - On a main-list branch: bare `↑a ↓b`, with zero counts omitted.
  - Otherwise: `(name ↑a ↓b)`.
  - `(name, no upstream)` when no upstream is configured;
    `(name, upstream gone)` when the configured ref is missing.
  - On a detached HEAD the tag or short hash takes the place of the name.
- **`· <default> ↑x ↓y`**: the local default branch vs its upstream. Shown
  only when the current branch ≠ the default branch, the local default branch
  exists, has an upstream, and at least one count is non-zero.
- **`· vs <default> ↑p ↓q`**: the current branch vs the default ref. Always
  shown when the current branch ≠ the default branch, `0 0` included: it tells
  you the branch is even with the default.
- **Error**: `⚠️ name ! <one-line error>`; the full error goes to stderr with `-v`.

### Definitions — ground truth is the git CLI

| Output | Equivalent git command | Notes |
| --- | --- | --- |
| `M` | `git diff --quiet` fails | Worktree vs index: content, deletion, mode (only when `core.fileMode=true`), type change, intent-to-add, gitlink HEAD ≠ index, unmerged |
| `S` | `git diff --cached --quiet` fails | Index vs HEAD tree. With an unborn HEAD, any index entry counts |
| `?` | `git ls-files --others --exclude-standard` non-empty | Excludes: system `core.excludesFile`, global (`$XDG_CONFIG_HOME/git/config`, then `~/.gitconfig`; default `$XDG_CONFIG_HOME/git/ignore`), repo `core.excludesFile`, `info/exclude`, per-dir `.gitignore`. A nested repo not in the index counts as untracked, as in git |
| `$` | `git rev-parse -q --verify refs/stash` | Loose or packed ref. The script checked only the loose file |
| ahead/behind | `git rev-list --left-right --count A...B` | Shallow boundaries are treated as roots |
| upstream | `git rev-parse @{u}` | `branch.<b>.remote` + `.merge`; `remote = .` means a local branch |
| default branch | `git symbolic-ref refs/remotes/<remote>/HEAD` | Remote = the current branch's upstream remote, else `origin`. Fallback: the first `GIT_DIRTY_MAIN_BRANCHES` entry that exists as `refs/heads/X` |
| default ref for "vs" | — | `refs/remotes/<remote>/<default>` if present, else `refs/heads/<default>` |

## Tasks

Each task lands with its tests green under `go test -race ./...`, plus
`gofmt -l .` empty, `go vet` and `staticcheck` clean. Each title carries its
phase: foundation, inspection (`internal/gitstat`), scan/render/modes, and
release/CI.

All inspection tests (Tasks 4–13) build fixtures in `t.TempDir()` with the
real `git` binary and compare against git's own output (the "Definitions"
table). They set `HOME`, `XDG_CONFIG_HOME` and `GIT_CONFIG_NOSYSTEM=1` to temp
values so the developer's config cannot leak in. They skip if `git` is not on
`PATH`. CI runners have git.

1. **[foundation] Scaffold.**
    - Files: `go.mod`, `main.go` (only `--version`; `var version = "dev"`),
      `.editorconfig` (tabs for `*.go`, 2 spaces for yaml/json/md, LF, final
      newline), `.gitignore` (gh-history's minus the report lines; plus
      `git-dirty`, `dist/`, `THIRD_PARTY_NOTICES.txt`, `bench.md`),
      `LICENSE` (MIT, Ismo Vuorinen, 2026), `renovate.json` (same preset as
      gh-history), `mise.toml`.
    - `mise.toml`: gh-history's tasks with `gh-history` → `git-dirty`. Drop
      `install`/`uninstall` (gh-extension specific). Add
      `goreleaser = "2.18.3"` to `[tools]`, because the shim currently has
      no version set.
    - Merge into the existing `mise.toml`, which holds
      `hyperfine = "latest"`. Pin it to `hyperfine = "2.0.0"` (the installed
      version): gh-history's rule is that an unpinned tool changes behavior
      without a commit, and Renovate's mise manager bumps pins.
    - `mise.lock` (already present, untracked) goes into `.gitignore`, as in
      gh-history.
    - Verify: `mise run build && ./git-dirty --version` prints `dev`, and
      `mise run lint` passes.

2. **[foundation] Config** — `internal/config/config.go`, `config_test.go`.
    - Struct with the defaults table above, plus `Interactive bool` (default true).
    - JSON load from `$XDG_CONFIG_HOME/git-dirty/config.json`, falling back to
      `~/.config/...`. `DisallowUnknownFields`; errors name the file and the
      key; a missing file is not an error.
    - Then the env overlay (`GIT_DIRTY_*`, `VERBOSE`, `NO_COLOR`), then flags.
      Precedence: defaults < file < env < flags. The script's order was
      defaults < env < sourced file; this change is recorded under accepted
      risks.
    - `~` expansion for `dir`. Validation: depth ≥ 0, maxDepth ≥ 1, no empty
      exclude entry.
    - Verify:
      - Table tests for precedence across all four layers.
      - An unknown key fails with the key named.
      - `"depth": -1` fails.
      - `GIT_DIRTY_EXCLUDE="a b"` replaces the default list and `-e c,d`
        appends to it.
      - `NO_COLOR=1` forces color off.

3. **[foundation] Flag parsing** — `main.go` `parseArgs`, `main_test.go`.
    - Short flags `-h -v -p -a -c -t -b -d N -e LIST -m N`, plus `--plain`
      and `--version`.
    - `expandShortFlags`: `-vac` → `-v -a -c`; `-d3` → `-d 3`, because a
      value-taking letter consumes the rest of its cluster.
    - Use gh-history's `fs.Parse` loop so flags after the positional directory
      are still parsed.
    - Help text matches the script's help text, updated for the new behaviors.
    - Exit codes: 2 on a usage error, 1 on a runtime error, 130 on SIGINT.
    - Verify with table tests: `-vac`, `-d3`, `-e a,b -e c`, `dir -v`, `-x`
      (usage error, exit 2), `-d` with no value (exit 2), `-d abc` (exit 2).

4. **[inspection] Open** — `open.go`.
    - Resolve the git dir: a `.git` directory, or a `.git` file containing
      `gitdir: <path>` (relative to the file), plus `commondir` for linked
      worktrees, via `dotgit.NewRepositoryFilesystem`.
    - Build `filesystem.NewStorageWithOptions` with
      `cache.NewObjectLRU(8 * cache.MiByte)`, not the 96 MiB default; with
      many workers the default can add up to GBs.
    - `Repo.Close()` closes the storage, which closes the packfile descriptors.
    - Verify:
      - Open succeeds on a plain repo, a `git worktree add` worktree and an
        absorbed submodule's gitdir file.
      - On Linux, `/proc/self/fd` shows the same descriptor count before and
        after 500 open/inspect/close cycles.

5. **[inspection] HEAD / branch** — `head.go`.
    - Order: symbolic ref → short branch name; else an exact tag match
      (lightweight, or annotated peeled to its commit; lexicographically first
      if several); else a 7-char short hash. An unborn HEAD gives the branch
      name with no commit.
    - Verify against `git symbolic-ref --short HEAD`,
      `git describe --tags --exact-match` and `git rev-parse --short HEAD` on
      fixtures: branch, lightweight tag, annotated tag, plain detached, unborn.

6. **[inspection] Ignore rules** — `ignore.go`.
    - Resolve the global and system `core.excludesFile` once per run, as an
      immutable slice shared read-only by all workers. go-git's
      `LoadGlobalPatterns` reads only `~/.gitconfig` and has no XDG default
      (verified in v5.19.3 `gitignore/dir.go`), so resolve it in-tree with
      `config.Paths(GlobalScope)` plus the `$XDG_CONFIG_HOME/git/ignore`
      default.
    - Per repo: the repo's `core.excludesFile`, then `info/exclude`.
    - Per directory: `.gitignore` parsed with `gitignore.ParsePattern(line,
      domain)` and pushed onto a stack while walking.
    - Verify against `git check-ignore -q`, path by path, for: a global
      ignore file at the XDG default, one set via `~/.gitconfig`, one set via
      `~/.config/git/config`, `info/exclude`, a nested `.gitignore`, negation,
      a dir-only pattern (`build/`) and an anchored pattern (`/x`).

7. **[inspection] Modified `M`** — `dirty.go` `modified()`.
    - For each index entry:
      - Skip `SkipWorktree` entries.
      - Unmerged (stage ≠ 0) → M.
      - `IntentToAdd` → M.
      - Gitlink: read the submodule's HEAD and compare it with the entry hash.
        If the submodule is not checked out, treat it as clean, as git does.
      - Otherwise `os.Lstat`. Missing → M. Type change → M. Executable bit
        differs while `core.fileMode=true` → M.
      - If size and mtime match the entry and mtime < the index file's mtime
        (the racy-git check), the entry is clean without hashing. Otherwise
        compute the blob hash (`plumbing.NewHasher(BlobObject, size)`, or the
        link target for symlinks) and compare.
    - Return at the first hit.
    - Verify against `git diff --quiet` on fixtures:
      - modified content, same-size modification in the same second as
        `git add` (racy), deletion, chmod +x with fileMode true and with false
      - file→symlink, `git add -N`, merge conflict
      - submodule moved to another commit, submodule not initialized
      - sparse-checkout entry missing on disk, `touch` with no content change
        (must be clean)

8. **[inspection] Staged `S`** — `dirty.go` `staged()`.
    - If the root cache-tree entry is valid (`Entries ≥ 0`), its entry count
      equals `len(idx.Entries)`, and its hash equals the HEAD commit's
      `TreeHash`, then nothing is staged, decided without reading any tree.
    - Otherwise walk the HEAD tree recursively
      (`object.NewTreeWalker(tree, true, nil)`) into a `path → (hash, mode)`
      map and compare it with the stage-0 index entries. Return at the first
      mismatch; a count mismatch also means staged.
    - Unborn HEAD: S if there are any index entries.
    - Verify against `git diff --cached --quiet`: staged add, modify, delete,
      mode change; unborn HEAD with and without entries; the cache-tree path
      after `git commit` (valid tree); the invalidated path after `git add`;
      `git add -N` only (not S).

9. **[inspection] Untracked `?`** — `dirty.go` `untracked()`.
    - Walk the worktree with `os.ReadDir`.
      - Skip `.git`.
      - Do not descend into directories the ignore stack matches.
      - Do not follow symlinks; a symlink is a file.
      - A subdirectory that contains a `.git` entry and is not an index
        gitlink counts as untracked as a whole.
    - A file not in the index set and not ignored → `?`. Return at the first
      hit. With `core.ignorecase=true`, index lookups are case-folded.
    - Check `ctx.Err()` every 256 entries.
    - Verify against `git ls-files --others --exclude-standard`:
      - an untracked file at the root and one nested; an empty untracked dir
        (not `?`)
      - a file inside an ignored dir that a `!` pattern tries to re-include
        (still ignored)
      - a nested repo, a symlink to a dir
      - ignorecase on a fixture with `core.ignorecase=true` and a case-renamed
        file

10. **[inspection] Stash `$`** — `dirty.go` `stashed()`, via `storage.Reference("refs/stash")`.
    - Verify: with a stash, after `git pack-refs --all` (packed), and after
      `git stash clear`.

11. **[inspection] Ahead/behind** — `aheadbehind.go`.
    - Commit nodes come from `commitgraph.NewGraphCommitNodeIndex` when
      `objects/info/commit-graph` or a commit-graph chain opens
      (`commitgraph/v2.OpenChainOrFileIndex`; close it afterwards).
      Otherwise from `NewObjectCommitNodeIndex`.
    - The walk: a `container/heap` priority queue ordered by
      generation number (descending), then commit time.
      - Commits outside the graph have generation ∞, which is safe: a
        commit-graph is closed under ancestry, so they cannot be ancestors of
        graph commits.
      - Flag bits: 1 = reachable from A, 2 = reachable from B. When a popped
        commit gains a new bit later (possible only under date ordering with
        clock skew), adjust its count and push it again.
      - Stop when every queued entry carries both bits.
      - Parents of commits listed in `shallow` are not followed.
    - Verify against `git rev-list --left-right --count A...B` on: equal
      tips, linear ahead, linear behind, diverged, a merge commit on one
      side, criss-cross merges, skewed `GIT_COMMITTER_DATE`, each of those
      with and without `git commit-graph write --reachable`, and a
      `git clone --depth 3` shallow clone.

12. **[inspection] Upstream + default branch** — `refs.go`.
    - Upstream from `branch.<b>.remote`/`.merge`; `remote = .` means the
      local branch. Configured but missing ref → "gone".
    - Default branch: `refs/remotes/<remote>/HEAD`, else the main-list
      fallback. Then compute both default comparisons as defined above.
    - Verify against `git rev-parse --abbrev-ref @{u}` and
      `git symbolic-ref refs/remotes/origin/HEAD`, on fixtures with a bare
      "remote" repo cloned twice: tracking set, no upstream, upstream
      deleted plus `fetch --prune` (gone), `remote = .`, origin/HEAD present,
      origin/HEAD absent with `master` only, current == default.

13. **[inspection] Inspect** — `inspect.go`: `Inspect(ctx, path, opts) Result`.
    - Composes tasks 4–12. Every error goes into `Result.Err`, wrapped with
      the stage name (`index:`, `head:`, `ahead/behind:`). Close runs on
      every path.
    - Verify:
      - A split-index fixture (`git update-index --split-index`) yields
        `Result.Err` containing "unknown extension", with no panic.
      - A corrupted `HEAD` file yields `Result.Err`.
      - A table test asserts all `Result` fields per fixture.

14. **[scan/render/modes] Scanner** — `internal/scan/scan.go`.
    - Discovery runs in one goroutine using `os.ReadDir`, applying excludes,
      `.ignore`, max depth, the no-symlink rule and stop-at-repo. It emits
      repo paths on a buffered `jobs` channel.
    - Workers: `runtime.GOMAXPROCS(0)` goroutines run `Inspect` and send to
      `results`. A single collector goroutine owns the result tree; no
      mutable state is shared.
    - Every send selects on `ctx.Done()`. `results` is closed after
      `wg.Wait()`.
    - Unreadable dirs become verbose warnings, never errors.
    - Verify:
      - Fixture-tree tests for each skip rule.
      - Ordering is deterministic after the sort.
      - Cancel mid-scan: `results` closes within 1 s, and a goroutine count
        check via `runtime.NumGoroutine` returns to baseline.
      - The tests pass under `-race`.

15. **[scan/render/modes] Tree model + plain renderer** — `internal/tree/tree.go`, `internal/render/plain.go`.
    - Build the node tree from the results. Siblings are sorted bytewise
      (`LC_ALL=C` parity). Apply verbose/`-d`/truncate rules and draw the
      `├── └── │` prefixes.
    - `Line` structs are shared with the TUI.
    - **Sanitize** every string that comes from the filesystem or git
      (dir names, branch/tag names, error text): control runes (C0, C1,
      DEL), and invalid UTF-8 → `\xNN`.
    - Colors are stdlib ANSI codes (the script's 32/31/33/35), on only when
      color is enabled, stdout is a char device and `TERM≠dumb`.
    - Verify:
      - Golden files in `testdata/` for: nested containers, verbose with `-d`,
        truncate off, branch display off, an error line, every branch-segment
        variant listed in "Output format".
      - A dir named `"a\x1b[2Jb"` renders as `a\x1b[2Jb` with literal
        backslashes.
      - No ANSI escapes appear when color is off.

16. **[scan/render/modes] Plain mode wiring** — `main.go`.
    - Header, legend, separator and summary go to stdout, as the script did.
    - The progress counter (`Found N…`) goes to stderr only when stderr is a
      TTY.
    - `signal.NotifyContext` handles SIGINT/SIGTERM and exits with 130.
    - A missing or non-directory base dir → exit 1 with a message.
    - Verify:
      - The binary run against a fixture tree, with
        `git-dirty --plain <dir> | cat`, is byte-identical to a golden file
        once the timing line is masked.
      - `git-dirty /nonexistent` exits 1.

17. **[scan/render/modes] TUI** — `internal/tui/model.go`.
    - bubbletea v2 model with:
      - a spinner and a `found/inspected` counter while scanning
      - a `viewport` showing the rendered lines, styled with lipgloss
        (styles map 1:1 to the plain colors)
      - `help` and `key` bindings: `↑↓ j k pgup pgdn g G` scroll, `f` toggle
        dirty-only, `b` toggle branch names, `r` rescan, `?` help, `q`/`ctrl+c`
        quit
    - Results arrive through a `tea.Cmd` reading the scan channel. A 50 ms
      tick batches them, so the tree re-renders at most 20×/s.
    - Rescan cancels the previous scan's context and drops messages tagged
      with an older scan generation.
    - Quit cancels the scan and waits for the workers.
    - Read-only: no actions.
    - The bubbletea v2 `View`/alt-screen API must be checked against the
      v2.1.0 godoc before writing this. Context7 failed to connect in the
      planning session.
    - Verify:
      - Unit tests drive `Update` with synthetic messages: a result batch, a
        stale-generation batch (ignored), the filter toggle (line count
        changes), rescan (generation increments), quit (returns `tea.Quit`).
      - `View` golden for a fixed model at 80×24.
      - A manual run on `~/Code` in a real terminal.

18. **[scan/render/modes] Mode selection + compat benchmark** — `main.go` `useTUI(cfg, stdin, stdout)`.
    - TUI when `Interactive` is set, `--plain` is not given, stdin and stdout
      are both char devices, and `TERM≠dumb`.
    - Verify:
      - Table test of the decision function.
      - **Equivalence on `~/Code`**: a script under `scripts/compare.sh`
        (dev-only, not shipped) maps each repo → letters + branch from the
        bash script's `-a` output and from `git-dirty --plain -a`. It must
        report zero mismatches; every mismatch is either fixed or traced to a
        documented intentional difference.
      - **Timing**: a `mise run bench` task (dev-only) runs
        `hyperfine --warmup 3 --export-markdown bench.md` over three
        commands: `git-dirty.sh ~/Code`, `git-dirty.sh -p ~/Code` and
        `./git-dirty --plain ~/Code`. Output goes to `/dev/null` so terminal
        rendering isn't timed. That measures the warm cache. A separate
        cold-cache pass uses `--prepare 'sync; echo 3 | sudo tee
        /proc/sys/vm/drop_caches'`; it needs sudo, so it runs only when you
        run it by hand. Record both tables in the PR description;
        `bench.md` is gitignored. Pass condition: the Go binary's warm mean
        is below `git-dirty.sh -p`'s, and hyperfine raises no outlier
        warning. If it warns, rerun on an idle machine.
      - Only after this passes: install the binary over
        `~/.local/bin/git-dirty`, keeping the script as `git-dirty.sh`.

19. **[release/CI] GoReleaser** — `.goreleaser.yaml`, `notices.tpl`.
    - gh-history's file with:
      - `binary: git-dirty`
      - `ldflags: -s -w -X main.version={{.Version}}`
      - an added `before.hooks` entry running
        `go run github.com/google/go-licenses/v2@v2.0.1 report .` with
        `--template notices.tpl`, redirected to `THIRD_PARTY_NOTICES.txt`
        (go-licenses v2.0.1 `report --template` exposes `.LicenseText`;
        verified in its `report.go`)
      - `release.extra_files`: `LICENSE` and `THIRD_PARTY_NOTICES.txt`
    - Rewrite gh-history's notices comment, because the dependency set is no
      longer only x/term. go-git is Apache-2.0; charm and the x/* modules are
      MIT/BSD.
    - Verify:
      - `goreleaser check`.
      - `goreleaser release --snapshot --clean` produces five binaries.
      - `dist/git-dirty_linux-amd64 --version` prints the snapshot version.
      - `THIRD_PARTY_NOTICES.txt` contains a license text for every module
        in `go list -deps -f '{{with .Module}}{{.Path}}{{end}}' . | sort -u`
        except the main module.
      - `go list -deps .` lists no `go-git/v5/plumbing/transport`.

20. **[release/CI] CI** — `.github/workflows/ci.yml`, `release.yml`, `codeql.yml`.
    - gh-history's three workflows with the same pinned action SHAs and
      `MISE_VERSION`. `release.yml` is unchanged apart from names.
    - Add a `test-os` job: matrix `macos-latest` and `windows-latest`,
      `actions/setup-go` with `go-version-file: go.mod`, `go test ./...`.
      Windows binaries ship, and the stat, mode, symlink and path-separator
      code is OS-sensitive.
    - Verify: `actionlint` passes locally; the first pushed branch shows all
      jobs green.

21. **[release/CI] Docs** — `README.md`, `CLAUDE.md`.
    - README: usage, flags, env vars, `config.json` schema with an example,
      the status legend, the output format, the TUI keys, and the
      differences from the bash script (the table above).
    - CLAUDE.md (gh-history style): commands, package layout, the
      plumbing-only import rule, and that git CLI equivalence is the test
      oracle.
    - Verify: every flag, env var and config key in the README is checked
      against `parseArgs` and `config` in a `/nitpicker docs` pass, with no
      findings.

## Adversarial hardening

- **complexity** — What was cut:
  - The notice for the old shell config: `~/.config/git-dirty/` does not
    exist on this machine.
  - A `-j` workers flag: `GOMAXPROCS` is the default, and Task 18 measures
    it.
  - `lipgloss/tree`: the prefix algorithm is about 20 lines and plain mode
    must stay stdlib-only, so one renderer feeds both modes.
  - A subtree-level cache-tree comparison: the root check plus the map
    fallback is enough.
  - `x/term`: the stdlib char-device check covers TTY detection.
  - gh-history's `.codacy.yaml`: not requested.

  What stays and why:
  - The custom dirty engine: the user chose it. The evidence is that
    go-git's directory hash is 24 zero bytes (`utils/merkletrie/filesystem/node.go`
    `calculateHash`), so `Worktree.Status()` cannot skip subtrees and walks
    every ignored file.
  - The in-tree ahead/behind walk: go-git has `MergeBase`/`IsAncestor` but
    no rev-list count.
- **review** — Edge cases now carried as explicit fixtures in Tasks 4–13:
  - racy-git same-second edits
  - `touch` with no content change
  - core.fileMode false
  - intent-to-add, unmerged, skip-worktree
  - gitlinks, both initialized and uninitialized
  - unborn HEAD, detached HEAD, annotated tags
  - linked worktrees
  - shallow clones
  - skewed commit dates
  - `remote = .`, gone upstreams
  - missing origin/HEAD
  - nested untracked repos, symlinked dirs
  - empty untracked dirs
  - re-include under an ignored dir
  - ignorecase

  The two default-branch segments got exact show/hide rules.
- **security** — The trust boundary is the content of cloned repositories,
  which may be hostile. Three changes came out of this lens:
  1. Escape sanitization at the render boundary (Task 15). A dir name or
      error string with ESC sequences would otherwise reach the terminal.
  2. No symlink following in discovery or in the untracked walk. That
      prevents loops and walks escaping the tree.
  3. Nothing is executed. The tool never runs `git`, so `core.fsmonitor`,
      hooks and filters in an untrusted repo cannot run code. That makes it
      strictly safer than the bash script, which runs `git diff` (fsmonitor
      applies) in every repo.

  A `gitdir:` file can point anywhere, but the tool only reads, so this is
  accepted.
- **errors / leaks** — Every per-repo failure ends up in `Result.Err`, is
  displayed, and the scan continues; nothing is swallowed, and `-v` prints
  the full chain. Released resources:
  - storage packfile descriptors (`Repo.Close`, fd-count test in Task 4)
  - the commit-graph index (closed in Task 11)
  - hashed files (`defer f.Close()`)
  - goroutines on cancel (Task 14 goroutine-count test)
  - the TUI scan on rescan or quit (Task 17)
- **migrations** — N/A: no schema or stored data. The config format change
  is a new file the user opted into, and no automatic conversion is planned.
- **concurrency** — The shared state is the global ignore patterns (built
  once before the workers start, read-only afterwards) and the result
  channel. The collector is the only goroutine that writes the tree. Each
  worker opens its own storage, so no go-git object is shared between
  goroutines. TUI messages carry a scan generation so stale results cannot
  merge. `-race` runs in CI (Task 20).
- **contract** — The public surface is the CLI. Kept compatible: flags,
  `GIT_DIRTY_*` env names and their defaults, the status letters. Changed
  on purpose, listed in the README (Task 21):
  - the config file format and path
  - the order of config precedence
  - the output layout (tree prefixes, counts)
  - `-p` is a no-op
  - `-d` and `-t` now do what their help text says

  Versioning is CalVer tags (`20*`), as in gh-history.
- **arch** — gh-history's layout: `main.go` plus `internal/`, nothing
  exported. The dependency direction is `main → config, scan, render, tui`;
  `scan → gitstat`; `render, tui → tree`. `gitstat` writes no output.
- **perf** — Per repo, the work is:
  - M: one `Lstat` per index entry, hashing only on a stat mismatch.
  - S: O(1) with a valid cache-tree.
  - `?`: proportional to non-ignored files, the same cost class as
    `git status`.
  - ahead/behind: proportional to the divergence when a commit-graph exists.

  Each check returns at its first hit. The 8 MiB object cache bounds memory
  per worker. TUI re-renders are capped at 20/s.

  One added task: the Task 18 hyperfine comparison against both script
  modes, warm and cold cache.
  The cost on a clean repo is a full stat pass, which `git status` pays too.
  It is accepted.
- **tests** — Every gitstat assertion checks the real git CLI's verdict on
  the same fixture rather than a hand-written expectation, so the tests
  cannot be tautological. Fixtures isolate `HOME`, `XDG_CONFIG_HOME` and
  `GIT_CONFIG_NOSYSTEM`. The end-to-end check is the Task 18 equivalence
  run against the bash script on `~/Code`. The TUI is tested through
  `Update` and `View` without teatest (an experimental dependency).
- **config** — The schema, env names, path and precedence are defined in
  Task 2. Unknown keys fail loudly. Nothing is secret.
- **privacy** — N/A: no personal data is stored or transmitted, and there is
  no network. Local paths and branch names are printed to the user's own
  terminal only.
- **a11y** — Status never depends on color: letters, icons and the `↑↓`
  counts carry it. `NO_COLOR` and `-c` are honored, and `--plain` gives
  linear screen-reader-friendly output.
- **i18n** — N/A: no locale scope is declared and the UI is English-only.
  Non-UTF-8 file names are escaped by the sanitizer (Task 15), not mangled.
- **observability** — A CLI with no service. Its signals are `-v` stderr
  logging (skipped dirs, full error chains) and the summary timing line.
  No telemetry is added.
- **license** — The dependency set grows from x/term only to go-git
  (Apache-2.0) plus charm (MIT). Added: generated `THIRD_PARTY_NOTICES.txt`
  shipped as a release file (Task 19), with a completeness check.

## Rollback / abort

- Greenfield: nothing in production depends on it. The bash script stays in
  place until Task 18's equivalence and timing checks pass. Install keeps it
  as `~/.local/bin/git-dirty.sh`, so rolling back is
  `mv git-dirty.sh git-dirty`.
- A broken release: delete the GitHub release and the CalVer tag
  (`gh release delete <tag> --cleanup-tag`), fix, retag.
- If the custom engine cannot reach zero mismatches in Task 18 for a class of
  repo, stop and report that class rather than ship it. Falling back to
  `Worktree.Status()` for that class is a user decision, not an
  implementation-time call.

## Open questions & accepted risks

- **Accepted — filters and LFS.** `git status` applies clean filters
  (autocrlf, LFS) before comparing; this tool hashes raw bytes. A file whose
  stat info no longer matches the index (for example after `touch`) in an
  LFS or autocrlf repo reports `M` where git, after refreshing its index,
  reports clean. The tool is read-only by design, so it cannot refresh the
  index. Documented in the README.
- **Accepted — assume-unchanged.** go-git's `index.Entry` has no
  assume-valid flag (checked in v5.19.3), so `git update-index
  --assume-unchanged` files are still checked. Rare.
- **Accepted — unsupported index extensions.** go-git returns
  `ErrUnknownExtension` for split-index (`link`) and sparse-index (`sdir`).
  Those repos show `⚠️ … ! index: unknown extension`. Not supported.
- **Accepted — `[include]`/`[includeIf]`** in git config are not followed by
  go-git's config decoder, so an `excludesFile` set only in an included
  file is missed.
- **Accepted — macOS Unicode normalization** (`core.precomposeunicode`) is
  not handled, so an NFD-named untracked file may be reported where git
  would not.
- **Accepted — SHA-256 object-format repos** depend on go-git v5's support.
  If unsupported they show as error lines, not wrong results.
- **Accepted — config precedence change** (env now beats the file). This is
  the conventional order, and the old file is not read at all.
- **Accepted — bare repos** under the scan root are not detected and are
  descended into, as in the script.
- **Accepted — go-licenses via `go run …@v2.0.1`** in the release hook.
  Renovate will not bump that pin; it is bumped by hand.
- **Assumption — TUI is a read-only browser** (scroll, filter, rescan). The
  answer chose "TUI default on a TTY" but did not choose actions; no git
  writes, fetch or shell-out are planned.
- **Assumption — "vs default" compares against the remote-tracking default**
  (`origin/main`) when it exists, because the separate "local default vs
  upstream" segment already shows how stale local `main` is.
- **Assumption — the zero-count rules**: the branch segment omits zero
  counts, and `· vs <default>` always prints both counts.
- **Assumption — module path** `github.com/ivuorinen/git-dirty`, MIT license
  (the script says MIT, © Ismo Vuorinen).
