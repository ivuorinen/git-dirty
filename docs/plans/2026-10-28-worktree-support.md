# Plan: list linked worktrees under their repository

Date: 2026-10-28
Status: APPROVED 2026-10-28, with `scripts/compare.sh` kept (see Decisions)

## Goal

When the scan finds a repository, read the linked worktrees its git dir
records (`worktrees/<id>/`), inspect each one, and show them as child lines
of that repository in the tree. Each worktree line is prefixed with 🌳, ahead
of its usual clean/dirty icon, so it cannot be mistaken for an ordinary
checkout.

```text
⚠️ /home/u/Code
├── ⚠️ ivuorinen
│   ├── ❌ git-dirty [M?] (feature/tree ↑2) · main ↓4 · vs main ↑5 ↓1
│   │   ├── 🌳 ✅ feat (feat, no upstream) · vs main ↑1 ↓0
│   │   ├── 🌳 ❌ hotfix [M] (hotfix ↑1) · vs main ↑1 ↓0
│   │   └── 🌳 ⚠️ old ! worktree: gitdir file points to non-existent location
│   └── ✅ dotfiles ↓3
```

## Decisions (user, 2026-10-28)

| Question | Answer |
| --- | --- |
| Icon | **🌳** (U+1F333). The user picked 🌿 and asked whether a tree fits better; it does: it names the feature, and it is a wide emoji like ✅ and ❌, so the line layout is the same. |
| A linked worktree the walk also finds inside the scan root | **Listed once, under its repository.** It stays at its own position only when no main repository in the scan lists it. |
| Toggle | **None; always on.** |
| The bash script | **Moves into this repository as `scripts/git-dirty.sh`**, so `scripts/compare.sh` and `mise run bench` keep a reference to compare against and the dotfiles copy can be retired. The copy is `local/bin/git-dirty` from the dotfiles branch `fix/audit-findings`, with six variables quoted for shellcheck (see Task 6): it has commit `50ce3b50`, which counts `.git` files (linked worktrees) and stops marking ↑ on branches with no upstream, as git-dirty does. The dotfiles `main` copy (`local/bin/git-dirty-sh`) lacks it. Both repositories are MIT, same author. |
| Stale worktree (directory gone) | **Shown with ⚠️** and git's wording, `gitdir file points to non-existent location`. |

## Scope & constraints

What exists today (read 2026-10-28):

- `scan.discoverer.walk` (`internal/scan/scan.go:107`) stops at any directory
  holding a `.git` entry and sends it to the workers. A linked worktree inside
  the root is therefore already listed, as an ordinary repository. A worktree
  nested in its repository (`.worktrees/x`) or outside the root is never seen.
- `gitstat.Inspect` already handles linked worktrees: `git.OpenWorktree`
  follows the `.git` file, then `commondir` (`internal/git/repository.go:28-66`,
  `TestOpen_LinkedWorktree`). The new work is discovery and display. Inspection
  stays as it is.
- Both outputs go through `tree.Tree` and `render.Line`. The TUI
  (`internal/tui/model.go:252-255`, `:306-310`) needs no change.
- Git 2.43 on disk (scratch probe): `worktrees/<id>/gitdir` holds the
  **symlink-resolved** absolute path of `<worktree>/.git`. A repository reached
  through a symlinked directory still records the real path. Ids collide by
  suffix: two worktrees named `feat` become `feat` and `feat1`. `git worktree
  list --porcelain` marks a missing one `prunable gitdir file points to
  non-existent location`.

Must not change:

- `internal/git` imports the standard library only (depguard).
- Nothing executes `git` at runtime. Tests use `git` as the oracle.
- The walk's skip rules (exclude names, `.ignore`, max depth, no symlinks, no
  descent into repositories) stay as they are. Worktrees are not walked. They
  come from the repository's own records, so those rules do not apply to them.
- Output for a tree without linked worktrees changes only in the legend line.

Out of scope, routed:

- `git.Open` reads `commondir` with an unbounded `os.ReadFile` and no
  regular-file check (`internal/git/repository.go:40`). A FIFO or a symlink to
  `/dev/zero` there hangs the inspection. This plan's reader does not touch
  `commondir`. Routes to `/nitpicker security`.

## Tasks

1. **Bounded link-file reader.** Add `readLinkFile(p, prefix string)` and
    `parseLinkFile(b []byte, prefix string)` to `internal/git/repository.go`.
    `readLinkFile` uses `Lstat` and accepts only a regular file of at most
    `maxLinkFile` (4096) bytes, reading through `io.LimitReader`. The parser
    takes the first line, trims it, strips `prefix` (`"gitdir:"` for a `.git`
    file, `""` for `worktrees/<id>/gitdir`), and rejects an empty path.
    `findGitDir` keeps its directory branch and calls `readLinkFile(p,
    "gitdir:")` for files, which replaces its inline parse. Add `FuzzLinkFile`
    to `internal/git/fuzz_test.go`: no panic, and a nil error implies a
    non-empty result with no newline, for both prefixes.
    - files: `internal/git/repository.go`, `internal/git/fuzz_test.go`
    - verify: `go test ./internal/git/` passes unchanged (every `OpenWorktree`
      test, linked worktrees included).
      `go test ./internal/git/ -run '^$' -fuzz '^FuzzLinkFile$' -fuzztime=30s -fuzzminimizetime=5s`
      finds nothing.

2. **List a repository's linked worktrees.** New `internal/git/worktree.go`:

    ```go
    type Worktree struct {
        ID   string // <id> under worktrees/: git's name for it
        Path string // the working tree the gitdir file names, without "/.git"
        Err  error  // why it cannot be inspected; Path may then be ""
    }

    // ListWorktrees lists the linked worktrees of the working tree at path,
    // in ID order. linked is true, with none listed, when path is itself a
    // linked worktree (its git dir has a commondir file).
    func ListWorktrees(path string) (wts []Worktree, linked bool, err error)
    ```

    `findGitDir(path)`. If `commondir` exists, the result is `linked`.
    `ReadDir(<gitdir>/worktrees)`, where `ErrNotExist` means none. Skip entries
    that are not directories. For each entry:

    1. `readLinkFile(admin/gitdir, "")`. A failure sets
        `Err = invalid gitdir file: …`.
    2. Join a relative path to the admin dir.
    3. Require base `.git`, else `invalid gitdir file`.
    4. Set `Path = Dir`.
    5. Call `findGitDir(Path)`. `ErrNotExist` sets
        `Err = errWorktreeMissing` ("gitdir file points to non-existent
        location").
    6. **Back-link check:** the git dir that `<Path>/.git` names must be the
        same directory as the admin dir, compared with `os.SameFile` because
        the recorded paths are resolved and the walk's are not. A mismatch sets
        `Err = worktree .git file names another git dir`.

    Error texts carry no repository path.
    - files: `internal/git/worktree.go`, `internal/git/worktree_test.go`
    - verify, against git:
      - `TestListWorktrees` adds worktrees outside the repository (two named
        `feat`, so ids `feat`/`feat1`), nested (`.worktrees/nested`) and as a
        sibling, then deletes one. Each live path matches
        `git worktree list --porcelain`. Each ID matches the basename of
        `git -C <wt> rev-parse --git-dir`. The deleted one has
        `errWorktreeMissing`, and porcelain says `prunable`.
      - `TestListWorktrees_Linked`: called on the sibling, `linked` is true and
        nothing is listed. Checked against `rev-parse --git-dir` ≠
        `--git-common-dir`.
      - A repository with no worktrees returns none, `false`, nil.
    - verify, cases git cannot produce:
      - `TestListWorktrees_Hostile`: the gitdir file is a directory; it is
        larger than 4096 bytes; it is a symlink to `/dev/zero` (skipped where
        `os.Symlink` fails or `/dev/zero` is absent), and the call must return
        with an error, not hang; the base is not `.git`; the worktree's `.git`
        names another repository's admin dir.
      - A gitdir file rewritten to a relative path gives the same `Path`. Git
        2.43 cannot write one, so this is hand-made.

3. **Scan: enqueue worktrees, hold walk-found linked worktrees, dedup.** In
    `internal/scan/scan.go`:
    - Add `Item.Worktree bool`: a linked worktree listed under its repository,
      whose `Rel` ends in its ID.
    - Add discoverer fields `claimed []os.FileInfo` and `linked []Item`. Only
      the discovery goroutine touches them.
    - `walk` calls a new `d.repo(dir, rel)` where it now sends the job.
      `repo` calls `gitrepo.ListWorktrees(dir)` (`internal/git` imported as
      `gitrepo`, as in gitstat):
      - `linked`: append to `d.linked` and send nothing yet.
      - Otherwise send the main job as today. On a listing error, send a
        `Warning` (`cannot list worktrees: …`).
      - For each worktree: with `Err` set, send `Kind: Repo`, `Worktree: true`,
        `Result{Path, Err: "worktree: " + Err}` straight to `out`, uninspected.
        Otherwise `os.Stat(Path)` into `claimed`, then send a job with
        `Rel = rel+[ID]` and `Worktree: true`.
    - After `d.walk` returns true, `d.sendLinked()` sends each held linked
      worktree whose directory is not `os.SameFile` with any claimed one. It
      runs in the discovery goroutine before the deferred `close(jobs)`.
    - files: `internal/scan/scan.go`, `internal/scan/scan_test.go`
    - verify:
      - `TestRun_Worktrees`: root/main (with a commit) has worktrees
        root/sibling, main/.worktrees/nested, one outside the root, and one
        stale. Expect items at `main`, `main/sibling`, `main/nested`,
        `main/<outside id>` and `main/<stale id>`, all with `Worktree` set
        except `main`. Expect **no** item at `sibling`. The stale one's error
        contains `worktree: gitdir file points to non-existent location`.
      - `TestRun_LinkedWorktreeAlone`: main repository outside the root. The
        worktree is listed at its own `Rel` with `Worktree` false.
      - `TestRun_WorktreesSymlinkedRoot`: same as `TestRun_Worktrees` with
        `Root` a symlink to the real directory (skipped if `os.Symlink`
        fails). `sibling` is still deduplicated. macOS CI hits this case
        without the symlink, since `t.TempDir()` is under `/var` →
        `/private/var`.
      - The existing cancel tests pass under `-race`.

4. **Tree and render.**
    - `internal/tree/tree.go`:
      - Add `Node.Worktree`, set in `Add`.
      - `repos` counts only items that are not worktrees, so "Found N git
        repositories" counts repositories.
      - `visible`: a repository is listed when it passes the dirty filter
        **or** any child is visible, so a clean repository with a dirty or
        failed worktree still shows under `-d`/`f`.
    - `internal/render/render.go`:
      - Add `IconWorktree = "🌳"`.
      - `Line` puts `Segment{Text: IconWorktree + " "}` before `stateIcon`
        when `n.Worktree`. It is a Plain segment: no new `Style`, no new ANSI
        or lipgloss entry.
      - The legend becomes
        `✅ Clean repo | ❌ Dirty repo | 🌳 Worktree | ⚠️ Directory / error`.
      - The comment "Icons, as in the bash script." no longer holds for the
        new icon. Reword it.
    - files: `internal/tree/tree.go`, `internal/render/render.go`,
      `internal/render/render_test.go`, `internal/render/testdata/*.golden`
    - verify: in `render_test.go`, add a `worktree(rel, res)` helper and three
      worktrees to `fixture()`:
      - under `ivuorinen/git-dirty`: one clean (`feat`, no upstream) and one
        stale (`Err: worktree: gitdir file points to non-existent location`);
      - under the clean `ivuorinen/dotfiles`: one dirty (`[M]`).

      Run `go test ./internal/render/ -update` and review the diff of all five
      goldens. Expect the legend line, the 🌳 lines in each mode, `dotfiles`
      now present in `dirty-only.golden` because of its dirty worktree, and
      the summary count unchanged. Expect
      `internal/tui/testdata/view-80x24.golden` unchanged: the TUI prints no
      legend.

5. **CLI.** In `main.go`:
    - `progress.report` counts only items that are not worktrees, so the
      stderr counter matches the summary. It still prints a worktree's error
      with `-v`.
    - `help` gets `🌳 = Linked worktree, listed under its repository` under
      "Status indicators". The TUI has no legend, so `-h` is where it is
      explained.
    - files: `main.go`, `main_test.go`, `testdata/plain.golden`
    - verify: in `TestRunPlain_Golden`, add
      `gitIn(t, alpha, "worktree", "add", "-q", "-b", "side", <root>/alpha-side)`.
      The regenerated golden lists `alpha-side` only under `alpha`, with 🌳.
      `Found 3 git repositories` is unchanged and stderr is empty. This runs
      real git, then scan → tree → render.

6. **Docs, and the bash script into the repository.**
    - `scripts/git-dirty.sh`: `local/bin/git-dirty` from the dotfiles branch
      `fix/audit-findings`, mode 755. The global `~/.shellcheckrc` (from the
      dotfiles) enables `quote-safe-variables`, and this repository has no
      shellcheck config, so the prek hook raised SC2248 on six unquoted
      variables. They are quoted: five numbers passed to `format_duration`,
      which reads an empty argument as 0 either way, and `wait
      "$progress_pid"`, always set from `$!` just before. No other change.
    - `scripts/compare.sh`:
      - The default `old` becomes `scripts/git-dirty.sh` next to it. The
        `~/.local/bin` lookups go: `~/.local/bin/git-dirty.sh` is now a
        dangling symlink and `~/.local/bin/git-dirty` is the Go binary, so
        today the script compares git-dirty with itself.
      - Its git-dirty parse already strips everything before ✅/❌, so a
        worktree line is read by its ID with no code change. The header
        comment says so, and that a worktree outside the directory, which the
        bash script never sees, shows as a `+` line.
    - `mise.toml` `bench`: `old=scripts/git-dirty.sh`, without the
      `~/.local/bin` fallback.
    - `README.md`:
      - Add worktree lines to the opening example.
      - Add a 🌳 row to "What a line shows".
      - In the scan paragraph (line ~148), say that linked worktrees are
        listed under their repository wherever they are on disk, ignoring
        exclude and depth, and that a walk-found one stays in place only when
        its repository is not in the scan. Mention the stale line.
      - Add a row to "Differences from the bash script".
      - Under Development, name `scripts/git-dirty.sh` as the bash script
        that `compare.sh` and `bench` run.
      - Add to Known limitations: per-worktree config
        (`extensions.worktreeConfig`, `config.worktree`) is not read (it is
        not: `rg config.worktree internal/` finds nothing), and
        `extensions.relativeWorktrees` is refused as an unknown extension.
    - `CLAUDE.md`:
      - The project line names `scripts/git-dirty.sh` instead of
        `~/.local/bin/git-dirty.sh`.
      - Add `worktree.go` to the internal/git list.
      - Describe the hold-and-dedup step in the internal/scan line.
      - Add `git worktree list --porcelain` and
        `rev-parse --git-dir`/`--git-common-dir` to the oracle list.
    - files: `scripts/git-dirty.sh`, `scripts/compare.sh`, `mise.toml`,
      `README.md`, `CLAUDE.md`
    - verify:
      - `diff` against `git -C ~/.dotfiles show
        fix/audit-findings:local/bin/git-dirty` shows only the six quoted
        variables, and the file is executable.
      - `mise run lint:all` (shellcheck on the new script, markdownlint and
        the rest) is clean.
      - `rg '\.local/bin' --glob '!docs/plans/**'` finds nothing.
      - `scripts/compare.sh` on a fixture holding a repository, a worktree
        inside the directory and one outside it: the outside worktree is the
        only mismatch, a `+` line.

7. **Gates and a real run.**
    - verify:
      - `mise run all` (golangci-lint, `test-race`, govulncheck, build) is
        clean.
      - `./git-dirty --plain ~/Code` before and after the change: record both
        wall times and confirm every pre-existing repository line is still
        present. Linked worktrees move under their repositories; nothing else
        moves.
      - `scripts/compare.sh ~/Code` reports no mismatches with the
        pre-change binary (`GIT_DIRTY_NEW`) and with the new one.

## Adversarial hardening

- **complexity**: examined every new mechanism against the decisions.
  - Cut:
    - a toggle (flag, env, config key), per the user;
    - a separate worktree count in the summary: worktrees are not counted, and
      `Summary` keeps its signature;
    - a new `Style` for the icon: an emoji ignores ANSI color, so it is a Plain
      segment;
    - display of `locked` worktrees: not asked for;
    - tree-level dedup with hidden nodes and an order-independent `Repos()`:
      replaced by holding walk-found linked worktrees in the single-threaded
      discoverer until the walk ends, so the tree gains one bool;
    - a gitstat passthrough wrapper: depguard restricts only internal/git's
      imports, so scan imports `internal/git` directly.
  - Removed: the `~/.local/bin` lookups in `compare.sh` and `bench`, which
    now find a dangling symlink or the Go binary. Task 1 deletes
    `findGitDir`'s inline parse instead of adding a second parser.
  - Kept: `compare.sh` with its parse unchanged, since it already reads a
    worktree line by its ID. The bash script comes along as a frozen copy;
    it gets no fixes here.
  - Kept although small: `FuzzLinkFile`, because CLAUDE.md requires a fuzz
    target for every new decoder.
- **review**: examined each input `ListWorktrees` and the discoverer can meet.
  Added explicit handling and a test for each:
  - id collisions (`feat1`): the tree is keyed by ID;
  - a stale directory;
  - a back-link mismatch;
  - a gitdir file whose base is not `.git`, or that is relative or empty;
  - the scan root as the main repository (worktrees become root children);
  - the scan root as a linked worktree (held, then sent at the end);
  - the main repository excluded or too deep (walk-found worktree stays in
    place);
  - a worktree inside an excluded directory (still listed under its
    repository);
  - `-t` off (prints the worktree's own path);
  - dirty-only with a clean repository and a dirty child (Task 4 `visible`
    change).
- **security**: trust boundary examined: `worktrees/<id>/gitdir` and the
  worktree's `.git` file are repository-controlled. New sinks:
  - File reads: bounded to 4096 bytes, regular files only. A FIFO, a device or
    a symlink to `/dev/zero` is refused before any read (Task 1).
  - `Inspect(path)`: reached only after the back-link `os.SameFile` check
    proves the directory's own `.git` points at this repository's admin dir.
    Added in Task 2 because without it a crafted gitdir file makes the tool
    inspect and print any directory.
  - Terminal output: the ID and the error text pass through `Sanitize` in
    `render.Line`. Error texts hold no path.
  - No exec.
  - The `commondir` hazard in `git.Open` is routed (see Scope).
  - `scripts/git-dirty.sh` runs `git` and sources
    `~/.config/git-dirty/config`, as it did from the dotfiles. It is a
    development aid run by hand: the release archives hold the binary only
    (`.goreleaser.yaml`, `formats: [binary]`), and forbidigo's no-exec rule
    is about Go code.
- **errors / leaks**: `ListWorktrees` keeps no handle open. The one file it
  opens per entry is closed by `defer` in `readLinkFile`. A per-entry failure
  becomes a visible ⚠️ line. A whole-listing failure becomes a `Warning`, shown
  with `-v` like an unreadable directory today. Worktree inspection reuses
  `Inspect`, which closes its repository.
- **migrations**: not applicable. No schema, no stored data, and config.json
  keys are unchanged.
- **concurrency**: examined the new discoverer state. `claimed` and `linked`
  belong to the discovery goroutine alone. `sendLinked` runs after `walk` in
  that goroutine and before `close(jobs)`, so every claim is in place before
  the dedup decision. Stale items go to `out`, which closes only after
  `wg.Wait()`, and the discovery goroutine is in `wg`. No new goroutine or
  channel. All sends use `send`, so cancellation holds. Verified by the
  existing cancel tests under `-race` (Task 3).
- **contract**: the public surface is the plain and TUI text and `-h`. Changes:
  - the legend line;
  - new 🌳 lines;
  - walk-found linked worktrees move under their repository and drop out of
    "Found N git repositories".

  No flag, env or config change. The project uses CalVer tags, so there is no
  semver bump. The only in-repo parser of this output, `compare.sh`, strips
  everything before ✅/❌, so it reads a worktree line by its ID (Task 6).
- **arch**: internal/git stays stdlib-only (`os`, `io`, `io/fs`, `errors`,
  `fmt`, `path/filepath`, `strings`). New edge: scan → internal/git, for
  discovery only. Inspection stays behind gitstat. tree, render and tui keep
  their roles, and the TUI is untouched.
- **perf**: examined the serial discovery goroutine. Per repository it adds
  `findGitDir`, which reads at most one small file and only for `.git` files,
  plus one `Lstat` (`commondir`) and one `ReadDir` (`worktrees/`, which fails
  fast with `ENOENT` in most repositories). Per worktree it adds one bounded
  read, two `Stat`s and one parallel `Inspect`, the same cost as a repository.
  Dedup does in-memory `SameFile` comparisons, held × claimed. Worktrees never
  inspected before now are, so trees with many worktrees scan longer. That
  comes with the feature, and Task 7 measures it.
- **tests**: examined each task's check.
  - Oracles: `git worktree list --porcelain` for paths and prunable entries,
    and `rev-parse --git-dir`/`--git-common-dir` for IDs and the linked case.
    A real `git worktree add` drives the end-to-end golden.
  - Hand-written cases: only states git cannot create (non-regular or oversized
    gitdir, back-link mismatch, relative gitdir under git 2.43). The reason is
    recorded with each.
  - Not tautological: scan tests assert that the walk-position item is absent,
    and goldens are reviewed as diffs.
  - SHA-256 is not run: listing reads no objects.
- **config**: not applicable. Always on per the user, with no new key or
  variable.
- **privacy**: worktree paths outside the scan root now print with `-t` off.
  They print only to the local terminal of the user who owns them, and
  nothing is sent anywhere. No change.
- **a11y**: the 🌳 symbol is the only marker on the line. The plain legend and
  the new `-h` line (Task 5) give it a text meaning, because the TUI shows no
  legend. Colors are unchanged.
- **i18n**: not applicable. English-only CLI with no locale handling.
- **observability**: a listing failure goes to stderr with `-v`. Worktree
  inspection errors print with `-v` as repository errors do.
  `progress.report` keeps that path (Task 5).

## Rollback / abort

Each task lands as its own commit on a feature branch and can be reverted
alone. Order of dependence: 1 → 2 → 3 → 4 → 5. Task 6 is independent except
for the README text. Nothing is persisted, so reverting restores today's
output exactly. Abort if Task 7 shows a pre-existing repository line missing
or changed. That would be a dedup false positive: stop and fix it before
merging.

## Open questions & accepted risks

- **Display name is the worktree ID**, git's name for it. It is unique per
  repository, but shows `feat1` after a collision and keeps its old name after
  `git worktree move`. `-t` off prints the real path. Accepted: an ID is what
  `git worktree lock/remove` take, and a basename would show two identical
  `feat` lines.
- **Worktrees are not counted** in "Found N git repositories". A tree whose
  sibling worktrees were counted before now shows a smaller number. Accepted:
  a worktree is not a repository. A ", N worktrees" suffix is a small follow-up
  if wanted.
- **TUI timing**: walk-found linked worktrees appear when discovery ends.
  When a worktree's result arrives before its repository's, the parent line
  shows ⚠️ for one 50 ms batch. Accepted: cosmetic, and only while scanning.
- **Worktree paths are followed through symlinks**, unlike the walk, as git
  does. Accepted: the back-link check pins every followed path to this
  repository's own admin dir.
- **A huge `worktrees/` directory** means as many inspections. Accepted: the
  same as walking a large tree. No cap, matching the walk.
- **A listing failure is visible only with `-v`.** Accepted: it matches how
  unreadable directories are reported.
- **Already-existing limitations, now documented (Task 6)**: `config.worktree`
  is not read, `extensions.relativeWorktrees` is refused, and bare
  repositories under the root are not detected. In that case their worktrees
  inside the root still list standalone.
- **A worktree outside the scanned directory is a `compare.sh` mismatch**:
  git-dirty lists it and the bash script never sees it. Accepted: it is the
  feature, and the script's header says so. On 2026-10-28 `~/Code` had 8
  stale worktrees in `ivuorinen/skills`, which print ⚠️ and which
  `compare.sh` skips, and one live worktree of `ivuorinen/f2b` outside
  `~/Code`: the only mismatch in Task 7's run.
- **Left in the dotfiles**: `local/bin/git-dirty.md` and
  `tests/git-dirty.bats`. The script's `-h` covers usage, and running the bats
  tests here would add bats to mise and CI.
