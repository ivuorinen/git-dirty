# Plan: TUI git actions, mass actions and settings

Date: 2026-11-07
Status: ACCEPTED 2026-11-07 — awaiting the go-ahead to implement

## Goal

Let the TUI fetch and pull the selected repository (`f`, `F`, `p`, `P`; the
uppercase key is the destructive variant and asks first), run the same actions
on every listed repository from an `m` dialog with a progress grid, move the
dirty filter to `d`, and edit the config file from an `s` settings screen, with
`-config PATH` choosing which file is loaded and saved.

Decisions taken before drafting:

- Actions run on the repository under a new cursor; `m` runs one on every
  compatible listed repository, warns first, and shows a grid of targets.
- `p` runs `git pull --ff-only --autostash`, not `--ff` (which merges on
  divergence).
- `F` and `P` ask y/n before running.
- Settings are in this plan; the config file stays JSON.

## Scope & constraints

Touches: `internal/tui` (`model.go` split by mode into `model.go`,
`actions.go`, `mass.go`, `settings.go`), a new `internal/action`,
`internal/config`, `main.go`, `README.md`, `CLAUDE.md`, the TUI golden files.

Must not change: `internal/git` (stdlib only, never executes), `gitstat`,
`scan`, plain-mode output, the config file's keys and format, the
`GIT_DIRTY_*` variables, existing flags.

Deliberate scope changes, stated so they are not discovered later:

- **The "never execute `git` or anything else at runtime" rule is reversed for
  `internal/action` only.** fetch and pull cannot be done without git: the
  network protocols, credential helpers, merge and rebase are git itself.
- README's "The browser is read-only: it never changes a repository." is
  removed; the package doc's "read-only" goes with it.
- `f` changes meaning (filter → fetch). Phase A's commit is `feat(tui)!:`,
  which releases a new minor version (CLAUDE.md, commit types).

Constraints: tests pass with git 2.56 and 2.39.2 (`mise.toml`) on Linux and
macOS, and with the `PATH` git on Windows; golangci-lint findings are fixed,
with the one runtime `exec` call site carrying a `//nolint` reason;
`.editorconfig`; markdownlint; git is the test oracle.

## Design

### Keys

| Key | Action |
| --- | --- |
| `↑`/`k`, `↓`/`j` | Move the cursor one line |
| `pgup`, `pgdn`/`space` | Move the cursor one page |
| `g`/`home`, `G`/`end` | First, last line |
| `d` | Show only dirty or failed repositories (was `f`) |
| `b` | Toggle branch names |
| `r` | Rescan |
| `f` | `git fetch --all` on the selected repository |
| `F` | `git fetch --all --prune` on the selected repository, after y/n |
| `p` | `git pull --ff-only --autostash` on the selected repository |
| `P` | `git pull --rebase=true` on the selected repository, after y/n |
| `m` | Mass actions |
| `s` | Settings |
| `?` | Full help |
| `q` | Quit; asks first while an action runs, because quitting interrupts git |
| `ctrl+c` | Quit now, interrupting a running git |

The viewport's own vertical bindings (`k`/`j`, `pgup`/`pgdn`/`space`, `u`/`d`
half page, `ctrl+u`/`ctrl+d`) are disabled: the cursor drives the offset
through `viewport.EnsureVisible`, so the viewport cannot scroll the cursor
off screen, and `d` no longer half-pages.

### Actions

| Key | argv | Asks | Compatible with | Mass run also skips |
| --- | --- | --- | --- | --- |
| `f` | `git fetch --all` | no | any repository, worktree or bare repository | a worktree listed under its repository: that repository's fetch updates the shared refs |
| `F` | `git fetch --all --prune` | yes | as `f` | as `f` |
| `p` | `git pull --ff-only --autostash` | no | not bare, readable (`Result.Err == nil`), on a branch, upstream configured and not gone | — |
| `P` | `git pull --rebase=true` | yes | as `p` | — |

An incompatible selection reports why in the status line (`pull: HEAD is
detached`) and runs nothing. A directory line reports `not a repository`.

### How git runs

- `internal/action` is the only package that runs a program, from one call
  site.
- argv is fixed per action. The repository is `cmd.Dir`, for bare repositories
  too, so no repository-derived string reaches argv, and git applies the
  user's own `safe.bareRepository` and `safe.directory` policy.
- Environment: `os.Environ()` without git's repository-local variables (the
  list `git rev-parse --local-env-vars` prints: `GIT_DIR`, `GIT_WORK_TREE`,
  `GIT_INDEX_FILE`, `GIT_COMMON_DIR`, `GIT_OBJECT_DIRECTORY`, …), plus
  `GIT_TERMINAL_PROMPT=0`. An exported `GIT_DIR` would otherwise point every
  pull at one repository.
- stdin is the null device and git has no controlling terminal (`Setsid` on
  Unix, `CREATE_NO_WINDOW` on Windows), so neither git nor ssh can draw a
  prompt into the TUI or read keys from it. A passphrase or unknown host key
  makes the action fail with git's own message.
- stdout and stderr go to one buffer that keeps the last 4 KiB. The status line
  and the failure list show the last non-empty line through `render.Sanitize`:
  `remote:` lines are written by the server.
- Cancel sends SIGINT on Unix (git removes its lock files on SIGINT) and kills
  on Windows. `cmd.WaitDelay` is 5 s, which also bounds a wait on an ssh
  `ControlPersist` master that inherited the output pipe.
- A scan and an action never overlap: actions are refused while a scan runs,
  `r`/`s`/`m`/`f`/`F`/`p`/`P` are refused while an action runs, and a finished
  action starts a rescan. The selection survives the rescan by path.

### Mass actions

- `m` lists the four actions, each with how many listed repositories it would
  run on. The current listing is the target set, so the dirty filter narrows
  it: what is on screen is what runs. Refused while a scan runs (the set would
  be incomplete).
- Choosing one shows its warning page: what it changes, the target count,
  "one repository at a time; this can take a long time", the bare repository
  count, and skip counts per reason. `y` starts, `esc` goes back. With zero
  targets there is nothing to start.
- Targets run sequentially, in display order.
- The grid has one cell per target: `□` waiting, `▣` running, `■` done. A
  failed cell is red `■` with color, `✗` without. A header line carries
  `fetch --all: 12/40 done, 2 failed`; a line under the grid names the running
  repository.
- `esc` while running stops after the current repository (nothing further
  starts). Stopping is non-destructive, so it does not ask.
- When the run ends, the failures (`path: last line`) are listed under the
  grid. `enter`/`esc` returns to the tree and rescans.

### Settings

- `s` opens a form over the **file layer**: defaults overlaid with the config
  file, without environment or flags. Rows: `dir`, `exclude`, `mainBranches`
  (both comma separated, as `-e`), `depth`, `maxDepth`, `checkStash`,
  `checkUntracked`, `color`, `truncate`, `showBranch`, `verbose`.
- `interactive` is not a row: `--plain` only turns it off and no variable
  turns it on, so saving it off would leave the file as the only way back.
- A row whose effective value differs (an environment variable or a flag
  overrides it) shows `overridden: <effective value>`.
- `↑`/`↓` pick a row, `enter` edits a text row or toggles a boolean, `enter`
  commits an edit, `esc` cancels it, `ctrl+s` saves, `esc` leaves (unsaved
  edits are discarded).
- Save: parse the rows → `Reconfigure(file)` supplied by `main`
  (`config.ApplyEnv` → `parseArgs` with the original arguments → `Normalize` →
  `checkDir` → a new `tui.Config`). An error is shown on the form and nothing
  is written. Then `config.Save`; an error is shown and nothing is applied.
  Then the new `tui.Config` replaces the old and a rescan starts.
- Refused while an action runs.
- Built from `bubbles/textinput`, already a dependency. Charm's form library,
  `charm.land/huh/v2` v2.0.3, was considered and not chosen: it adds six
  direct modules (`catppuccin/go`, `hashstructure`, `x/xpty`, `x/exp/*`, with
  `creack/pty` and `atotto/clipboard` indirect) for eleven fields.

### `-config` and saving

- `-config PATH` (also `--config PATH`, `-config=PATH`) is found by a pre-pass
  before `config.Load`, because the flags are applied on top of the loaded
  file. It is also registered in the `FlagSet` so the main parse accepts it,
  and `config` joins `longNames` so `-config` is not split into `-c -o …`.
- A missing file loads the defaults, as the default path does, so a test can
  point `-config` at a fresh path and a save creates it. `-h` and the settings
  page show the path in use.
- `config.Save(path, cfg)` writes only the keys whose values differ from
  `Defaults()`, so a later change of a default still reaches the user. It
  follows a symlinked config file to its target (dotfile managers link it),
  writes a temporary file in the target's directory and renames it over the
  target, keeps an existing file's mode (a new one is `0600`), and creates the
  directory when missing.

## Tasks

Five phases, one commit each, `mise run all` green at the end of every phase.

### Phase A — cursor and `d` (`feat(tui)!: add a cursor and move the dirty filter to d`)

- **A1. Keymap.** The dirty filter moves to `d`; `f` is unbound for now; the
  viewport's vertical bindings are disabled.
  - files: `internal/tui/model.go`
  - verify: `model_test.go`: `d` toggles `[dirty only]` in the status line;
    `f` leaves it unchanged; `d` leaves `vp.YOffset()` unchanged.
- **A2. Cursor.** `cursor int` and `selected string` (the node path); styled
  lines are cached in `refresh()` and only the `"> "`/`"  "` gutter is redrawn
  on a cursor move; `k`/`j`/`↑`/`↓` move one line, `pgup`/`pgdn`/`space` one
  page, `g`/`G`/`home`/`end` to the ends; `vp.EnsureVisible` follows the
  cursor.
  - files: `internal/tui/model.go`
  - verify: tests: the cursor clamps at both ends; after moving past the
    viewport height the cursor row is between `YOffset()` and
    `YOffset()+Height()`; a `tea.WindowSizeMsg` that shrinks the window keeps
    it visible.
- **A3. Selection by path.** After every `refresh()` (batch, filter toggle,
  rescan) the cursor goes to the line whose `Node.Path == selected`, else the
  index is clamped; only cursor keys change `selected`.
  - files: `internal/tui/model.go`
  - verify: tests: a batch that inserts lines above the selection keeps the
    cursor on the same path; selecting a clean repository, pressing `d` twice
    puts the cursor back on it.
- **A4. Golden file and docs.** Regenerate `testdata/view-80x24.golden`
  (gutter); README keys table.
  - files: `internal/tui/testdata/view-80x24.golden`, `README.md`
  - verify: `go test ./internal/tui/ -update`, review the diff;
    `mise run all`.

### Phase B — single actions (`feat(tui): fetch and pull the selected repository`)

- **B1. Action kinds.** `internal/action`: `Kind` (`Fetch`, `FetchPrune`,
  `Pull`, `PullRebase`), its argv, `Asks()`, `Warning()` text, and
  `Check(*gitstat.Result) error` with the compatibility rules above.
  - files: `internal/action/action.go`, `internal/action/action_test.go`
  - verify: table test over `gitstat.Inspect` results of fixtures made with
    git (bare repository, detached HEAD, no upstream, gone upstream,
    unreadable, ordinary): `Check` accepts or refuses per the table.
- **B2. Runner.** `Run(ctx, dir, Kind) Result{Output, Err}` as in "How git
  runs", with `proc_unix.go` (`Setsid`, SIGINT) and `proc_windows.go`
  (`CREATE_NO_WINDOW`, kill). One call site with
  `//nolint:forbidigo,gosec // the TUI's fetch and pull actions run git by design (CLAUDE.md)`.
  - files: `internal/action/run.go`, `proc_unix.go`, `proc_windows.go`,
    `run_test.go`
  - verify: oracle tests, with `GIT_CONFIG_GLOBAL` set to a test file and
    `GIT_CONFIG_NOSYSTEM=1`:
    - `Fetch` after an upstream commit: `git rev-list --count HEAD..@{u}` is
      1.
    - `FetchPrune` after the upstream deletes branch `x`:
      `git show-ref --verify refs/remotes/origin/x` exits 1; plain `Fetch`
      leaves it.
    - `Pull` with upstream changing line 1 and an uncommitted local change on
      line 10 of the same file: `HEAD` equals `@{u}` and the file has both
      changes. This only passes with `--autostash`; git refuses the merge
      without it.
    - `Pull` on a diverged branch fails, and `HEAD` and
      `git rev-list --merges HEAD` are unchanged. With `--ff` it would merge.
    - `PullRebase` on a diverged branch: `HEAD~1` equals `@{u}` and
      `git rev-list --merges HEAD` is empty.
    - With `GIT_DIR` exported to another repository, `Fetch` updates the
      target's refs and not the other's.
    - An `httptest` remote answering 401: `Run` fails within a 10 s context
      instead of waiting on a prompt.
    - With `safe.bareRepository=explicit` in the test's global config,
      `Fetch` on a bare repository fails, and so does `git fetch --all` run
      there by the test; without it, both succeed.
    - Canceling the context during a fetch from an `httptest` remote that
      stalls: `Run` returns within `WaitDelay` plus 2 s.
    - `PATH` without git: `errors.Is(err, exec.ErrNotFound)`.
    - Every name `git rev-parse --local-env-vars` prints is in the strip list
      (a superset check, since the list grows between git releases).
    - 100 KiB of output keeps the last 4 KiB.
- **B3. TUI single actions.** `f`/`F`/`p`/`P` on the selected node; `Check`
  failure → status reason; `Asks()` → confirm page (warning, path, `y`/`n`,
  `esc`); spinner and `fetch <path>…` while running; `fetch <path>: ok` or
  `fetch <path>: failed: <last line>` after, then a rescan. Refusals while
  scanning and while busy. `tui.Config.Run` is the runner (`nil` means
  `action.Run`), and results carry an action generation number.
  - files: `internal/tui/actions.go`, `model.go`, `model_test.go`
  - verify: tests with a fake runner: `f` calls it with the selected path and
    `Fetch`; `P` shows the confirm page, `n` runs nothing, `y` runs; keys
    during a run (the fake blocks on a channel) are refused; keys while
    scanning are refused; a result restarts the scan (`Start` called again);
    a result line containing `"\x1b[31m"` is shown escaped.
- **B4. Quit during an action.** `q` asks y/n, `ctrl+c` does not; both cancel
  the action context, and `Model.stop()` waits for the runner command to
  return.
  - files: `internal/tui/actions.go`, `model.go`
  - verify: test: a fake runner that blocks until its context is done; `q`,
    `y` returns `tea.Quit`, and `stop()` returns after the fake saw the
    cancel.
- **B5. Docs.** README keys and an "Actions" section (argv table, no prompts,
  needs ssh-agent or a credential helper, runs the repository's hooks and
  helpers as git does), the read-only sentence removed; the `internal/tui`
  package doc; CLAUDE.md: `internal/action` under Architecture, and the
  "Never execute" rule rewritten (only `internal/action` runs git, only on a
  key press, fixed argv, stripped environment, no terminal).
  - files: `README.md`, `CLAUDE.md`, `internal/tui/model.go`
  - verify: `prek run markdownlint --files README.md CLAUDE.md`;
    `mise run all`.
- **B6. Lint message.** Approved on 2026-11-07, as the rule against editing
  lint configuration requires. Change the forbidigo message in
  `.golangci.yml` from "git-dirty never executes programs at runtime; only
  tests may run git" to one naming `internal/action`.
  - files: `.golangci.yml`
  - verify: `mise run lint`.

### Phase C — mass actions (`feat(tui): run fetch and pull on every listed repository`)

- **C1. Targets.** `massTargets(lines, Kind) (targets []*tree.Node, skipped
  map[string]int)`: display order, nodes with a repository, `Check`, and the
  fetch skip for worktrees listed under their repository.
  - files: `internal/tui/mass.go`, `mass_test.go`
  - verify: a tree built from scan items (bare repository with worktrees,
    detached, no upstream, unreadable) gives the expected targets and skip
    counts per kind; with the dirty filter on, only listed nodes are targets.
- **C2. Menu and warning.** `m` menu and warning page, refused while scanning
  or busy; zero targets cannot start.
  - files: `internal/tui/mass.go`
  - verify: tests: `m` while scanning shows the refusal; menu counts match
    `massTargets`; `y` on zero targets starts nothing; golden file of the
    warning page at 80×24 without color.
- **C3. Sequential runner.** One target `active` at a time; each completion
  starts the next unless stopping; `exec.ErrNotFound` ends the run with
  `git not found on PATH`; `esc` stops after the current one.
  - files: `internal/tui/mass.go`
  - verify: fake runner tests: call order equals display order; never two
    calls outstanding; after `esc` the remaining cells stay waiting and the
    runner is not called again; `ErrNotFound` produces one call.
- **C4. Grid and result view.** Cells wrap at the viewport width (two columns
  each, at least one per row), header counts, running path, failures listed
  under the grid after the run, `enter`/`esc` back to the tree with a rescan.
  - files: `internal/tui/mass.go`
  - verify: golden files for a mid-run grid and a finished run with one
    failure (80×24, no color, failed cell `✗`); a width-1 render has one cell
    per row; `esc` after the run calls `Start`.
- **C5. Docs.** README `m` and mass actions; CLAUDE.md `internal/tui` bullet.
  - files: `README.md`, `CLAUDE.md`
  - verify: markdownlint; `mise run all`.

### Phase D — `-config` and saving (`feat: add -config to choose the config file`)

- **D1. Config layers.** `config.LoadFile(path)` (defaults + file),
  `config.ApplyEnv(Config)` (today's `applyEnv`, on a copy),
  `config.Load(path)` = both, `""` meaning `Path()`.
  - files: `internal/config/config.go`, `config_test.go`
  - verify: existing tests pass through `Load("")`; `Load(custom)` reads
    `custom` and ignores the XDG file.
- **D2. `-config` flag.** The pre-pass in `main` (respects `--`, sees values
  after a positional), the `FlagSet` entry, `longNames`, a help line and the
  path in use in `-h`.
  - files: `main.go`, `main_test.go`
  - verify: `-config x`, `--config x`, `-config=x` and `~/Code -config x`
    load `x`; `-c` still toggles color; `-- -config` is a directory argument;
    `-config` without a value exits 2.
- **D3. Save.** `config.Save` as in "`-config` and saving".
  - files: `internal/config/config.go`, `config_test.go`
  - verify: `LoadFile(Save(c))` equals `c` for a non-default `c`;
    `Save(Defaults())` writes `{}`; a symlinked `config.json` is still a
    symlink and its target holds the new content (skipped on Windows); an
    existing `0600` file stays `0600` (skipped on Windows); a failed write
    leaves no temporary file.

### Phase E — settings screen (`feat(tui): edit the config file from the settings screen`)

- **E1. Settings page.** As in "Settings"; `tui.Config` gains the config
  path, the file layer, `Reconfigure` and `Save`, supplied by `main`.
  - files: `internal/tui/settings.go`, `settings_test.go`, `main.go`
  - verify: fake `Reconfigure`/`Save`: `depth` set to `x` shows an error and
    `Save` is not called; a valid `maxDepth` edit calls `Save` with only
    `maxDepth` changed from the file layer, then `Start` with the new
    options; a `Reconfigure` error (missing `dir`) writes nothing; a row whose
    effective value differs shows `overridden:`; there is no `interactive`
    row; `s` while an action runs is refused.
- **E2. Docs.** README configuration (settings screen, `-config`, save writes
  only non-default keys), CLAUDE.md config bullet.
  - files: `README.md`, `CLAUDE.md`
  - verify: markdownlint; `mise run all`.

### Final verification

`mise run all` (lint, race tests, oldest-git tests, govulncheck, build), then
by hand in a scratch tree of fixture repositories: each key on a repository, a
worktree, a bare repository and a directory; an `m` run with the grid; a
passphrase key without an agent fails with git's message and nothing drawn
into the TUI; a settings save, then a restart with `-config` on a temporary
path.

## Adversarial hardening

- **complexity**: examined every task against the four requested features.
  Cut: a separate result page (failures go under the grid), a confirmation
  for stopping a mass run (stopping only skips work), a discard prompt on
  leaving settings, per-repository re-inspection after an action (the
  existing `startScan` plus selection-by-path covers it), half-page cursor
  keys, the YAML switch, and parallel mass runs. Rejected `huh/v2` for six
  direct modules, and `tea.ExecProcess` for single actions (mass runs need the
  background runner anyway, and one runner is less code than two). Kept the
  local-environment strip (an exported `GIT_DIR` would point a mass pull at
  one repository N times) and the no-terminal launch (otherwise ssh prompts
  into the TUI).
- **review**: edge cases now in tasks: directory or root line selected;
  detached HEAD; unborn branch (allowed when an upstream exists); gone
  upstream; unreadable repository (fetch allowed, since git can read formats
  `internal/git` cannot, pull refused); worktree whose directory is missing
  (fails with git's message); empty listing; zero targets; the selected line
  hidden by the filter; lines inserted above the selection during a scan;
  resize; a grid narrower than two columns; no remotes (`fetch --all` exits
  0); a `-config` value after the directory or after `--`.
- **security**: the trust boundary moves. git now runs in directories the
  walk found, so each repository's configuration can run programs (hooks such
  as `post-merge` and `reference-transaction`, `core.fsmonitor` during
  `--autostash`, `core.sshCommand`, credential helpers). Changed: argv carries
  no repository-derived text; bare repositories run through `cmd.Dir`, so the
  user's `safe.bareRepository` still applies instead of being bypassed with
  `--git-dir`; the environment is stripped of repository-local variables;
  server text is sanitized before display; `exec.LookPath` refuses a `git`
  found through `.` in `PATH`. The walk does not enter repositories, so a
  bare repository inside another repository's checkout is never listed. The
  residual risk is in Open questions.
- **errors / leaks**: each `Run` waits on its process (`cmd.Run`); output is
  capped at 4 KiB; `WaitDelay` bounds a wait held open by a grandchild;
  cancel is SIGINT so git cleans its lock files; `stop()` cancels and waits
  for the runner as it does for the scan; `ErrNotFound` ends a mass run
  instead of failing N times; `Save` removes its temporary file on every
  error path and only renames after `Sync` and `Close` succeed; a save error
  or a `Reconfigure` error leaves both the file and the session unchanged.
- **migrations**: not applicable as a schema or data migration. The config
  file keeps its format and keys, so files written by `Save` load in older
  builds. Recorded: `Save` drops keys equal to the default.
- **concurrency**: shared state stays inside `Update`. Runner results carry
  an action generation number, as scan batches do, so a result arriving after
  a quit or restart is dropped. A scan and an action never overlap. Mass
  targets run one at a time, because worktrees of one repository share
  remote-tracking refs and their lock files: concurrent fetches fail with
  "cannot lock ref".
- **contract**: CLI flags gain `-config` (additive). The `f` key changes
  meaning, hence `feat(tui)!:` on phase A. The config keys are unchanged.
  `tui.Config` and `config.Load`'s signature change, both internal. The
  README read-only promise is withdrawn in the same phase that breaks it
  (B5).
- **arch**: `internal/action` imports `gitstat` and the standard library;
  nothing imports it except `tui`. `internal/git` stays exec-free and stdlib
  only. `tui` imports `config` for the settings form, and there is no cycle
  (`config` imports nothing internal). `checkDir` and `parseArgs` stay in
  `main` and reach the TUI through `Reconfigure`, so no CLI parsing moves into
  `tui`. `handleKey` splits per mode (browse, confirm, mass, running,
  settings) to stay under `gocyclo`'s threshold.
- **perf**: a cursor move redraws only the gutter of cached styled lines, not
  `render.Line` for every row. The grid render is O(targets). Each finished
  action costs one full rescan, the same work as `r`. Plain mode and `mise run
  bench` are untouched.
- **tests**: no test compares argv against a literal list, which would be a
  tautology. Each argv is proven by behavior: autostash by the line-1/line-10
  fixture, `--ff-only` by the unchanged diverged HEAD, `--rebase=true` by the
  merge-free history, `--prune` by the deleted ref. The TUI state machine is
  tested with a fake runner, and git is the oracle for `Check`'s inputs.
  Global and system git config are isolated in action tests. The two cases
  CI cannot exercise (no controlling terminal, ssh prompts) are in the manual
  checklist. The local-env list test is a superset check, so it passes on
  both pinned git versions.
- **config**: `-config` documented in `-h` and README; `Save` writes only
  non-default keys; `DisallowUnknownFields` unchanged; precedence (defaults <
  file < env < flags) unchanged and visible in the settings form through
  `overridden:`.
- **privacy**: not applicable. No personal data is stored or sent. git output
  is shown on screen only and never written to disk.
- **a11y**: the cursor is a text gutter, visible without color; a failed cell
  has its own glyph without color; every action has a key and is listed in the
  full help; confirm pages take `y`/`n`/`esc`.
- **i18n**: not applicable. The project has no locale handling and declares
  no locale scope; strings stay English.
- **observability**: not applicable as logs or metrics, since this is a local
  CLI. The run's signals are on screen: every failure is listed with its path
  and git's last line, and the header counts done and failed, so no failure is
  silent.

## Rollback / abort

- Each phase is one commit and reverts on its own in reverse order. C needs
  B, and E needs D.
- No file format changes: a config file written by `Save` holds only existing
  keys and loads in older builds.
- Stopping after any phase leaves a releasable tree. A alone (cursor and `d`)
  and D alone (`-config`) are useful without the rest.
- Repository state an action can leave behind: an interrupted `P` leaves a
  rebase in progress (`git rebase --abort`); an autostash that conflicts on
  re-apply keeps the changes in `git stash list`.

## Open questions & accepted risks

- Task B6 (the forbidigo message in `.golangci.yml`) was approved on
  2026-11-07.
- Running git executes what the repository's configuration names (hooks,
  `core.fsmonitor`, `core.sshCommand`, credential helpers), as running git
  there by hand would. A bare repository in a plain folder, for example an
  unpacked archive, could carry a hostile configuration. The mass warning
  shows the bare repository count and README says to run actions only on
  repositories you trust. Accepted.
- No interactive credentials: a key needing a passphrase, an unknown host key
  or a typed password fails. ssh-agent or a credential helper is required.
  GUI credential helpers may still open a window per repository during a mass
  run.
- Mass runs are sequential (reason under concurrency). Parallelism would need
  deduplication by common git dir and a connection limit; it can be revisited
  if runs are too slow.
- The grid has a fourth visual state: a failed done cell. Red with color,
  `✗` without, so a failure is never shown as plain done.
- Mass targets are the listed repositories, so the dirty filter narrows them.
- `P` on uncommitted changes fails with git's refusal unless
  `rebase.autoStash` is set. Not pre-filtered: git is the authority, and
  predicting it would mean reading every config layer for that key.
- Quitting during an action interrupts git. A rebase is left in progress; a
  fast-forward interrupted during checkout can leave a partly updated working
  tree that `git status` shows.
- No per-repository timeout. A remote that hangs holds a mass run until
  `ctrl+c`. A fixed timeout would also kill legitimate long fetches.
- `□ ▣ ■` are East Asian ambiguous width and misalign where a terminal draws
  them two columns wide.
- With `safe.bareRepository=explicit`, fetching a bare repository fails with
  git's refusal. This is deliberate: the user's policy wins over convenience.
- `interactive` is not editable in settings (reason under Settings).
- `Save` drops keys whose value equals the default. A hand-written file loses
  those lines on the first save; the values are unchanged.
- A missing `-config` file loads defaults, so a mistyped path silently uses
  defaults. `-h` and the settings title show the path in use.
- Leaving settings with `esc` discards unsaved edits without asking.
- `m` is refused while a scan runs, and every action triggers a full rescan
  rather than re-inspecting one repository.
