# Design: list bare repositories with their worktrees

Date: 2026-11-01
Status: APPROVED 2026-11-01, implemented on feat/bare-repositories

## Goal

List a bare repository as one line marked 🪵, with the number of worktrees
it records, and its linked worktrees as child lines under it, as a working
tree's worktrees are listed today.

```text
⚠️ /home/u/Code
├── 🪵 plain.git (2 worktrees)
│   ├── 🌳 ❌ plain-feat [M] (feat, no upstream) · vs main ↑0 ↓0
│   └── 🌳 ✅ plain-main
├── 🪵 lonely.git (0 worktrees)
└── ✅ dotfiles
```

## Decisions (user, 2026-11-01)

| Question | Answer |
| --- | --- |
| Icon | **🪵** (U+1FAB5, log). A house was rejected as not capturing a bare repository; the log is the trunk the 🌳 worktrees branch from. It is a wide emoji like ✅, ❌ and 🌳. It is Unicode 13.0 (2020), so older emoji fonts print a box. |
| The bare repository's own line | **Name and worktree count**: `🪵 plain.git (2 worktrees)`, `(1 worktree)`, `(0 worktrees)`. No status letters, branch or default-branch parts: a bare repository has no working tree. The count is every worktree the repository records, so it stays the same when the dirty-only filter hides some. |
| Where bareness is decided | **`gitstat.Inspect`**, where the repository is already opened, in a worker (approach A). Rejected: deciding during discovery (B), which adds a config read for every repository to the serial walk and leaves `Inspect` reporting `[S?]` for a bare `.git`; and listing a bare repository as a marked directory (C), which neither counts it as a repository nor covers a `.git` with `core.bare`. |
| Which worktrees are listed under it | **Only the records in its `worktrees/` folder**, as `git worktree list` reads them. A worktree folder moved with `mv` is listed under the repository as the stale record and on its own where the walk finds it; `git worktree repair` fixes both. A folder whose record was pruned is listed on its own. |

## Current behaviour (probe, 2026-11-01)

| Layout | git | git-dirty before this change |
| --- | --- | --- |
| `plain.git/` from `git clone --bare`, worktrees in other folders | bare | ⚠️ directory, walked into (`objects/`, `refs/`, ...); its worktrees are listed as separate repositories |
| `proj/.bare/` and `proj/.git` containing `gitdir: ./.bare`, worktrees inside `proj/` | bare; `git status` fails with "this operation must be run in a work tree" | `❌ proj [S?]`, a false dirty state; its worktrees are listed under it |
| `dotgit/.git/` that is a bare repository (`core.bare = true`) | bare; `git status` fails | `❌ dotgit [S?]`, a false dirty state |
| `lonely.git/` with no worktrees | bare | ⚠️ directory, walked into |

## Definitions (git 2.43.0 source)

**Git directory.** `is_git_directory` in `setup.c`: `HEAD` passes
`validate_headref` (`path.c`), and `objects/` and `refs/` are accessible.
`validate_headref` accepts a symlink whose target starts with `refs/`, or a
file whose first 255 bytes start with `ref:`, optional whitespace and
`refs/`, or with a hex object ID; `get_oid_hex` reads the first 40
characters, so a SHA-256 ID passes too.

**Bare.** A repository has no working tree when either:

- the path is itself the git directory (`setup_bare_git_dir`), or
- the path holds a `.git` entry whose repository sets `core.bare = true`
  (`setup_discovered_git_dir`, `is_bare_repository_cfg > 0`).

A linked worktree is never bare: when the git directory has a `commondir`
file, `check_repository_format_gently` ignores the common config's
`core.bare` and `core.worktree` (`has_common`). That is why the worktrees of
a bare repository have working trees.

The test oracle for "bare" is `git rev-parse --is-inside-work-tree` printing
`false` from the path. `--is-bare-repository` is not used: it prints `false`
for a git directory with `core.bare = false`, where `git status` still fails.

## Design

### internal/git

- `IsGitDir(dir string) bool` applies the definition above, with two
  differences from git. It does not follow `commondir`, so a linked
  worktree's admin directory (`worktrees/<id>/`) is not a repository; the
  walk never reaches one, because it stops at the repository holding it. It
  does not read `GIT_OBJECT_DIRECTORY`.
  - `HEAD` is checked with `Lstat`: a symlink by its `Readlink` target
    only, never followed; a regular file opened with `OpenRegular` and read
    up to 255 bytes; anything else (FIFO, device, directory) fails.
  - `objects/` and `refs/` are checked with `Stat` and `IsDir`, following
    symlinks as git's `access(2)` does. Nothing in them is read.
  - Any error, permission denied included, means "not a git directory".
- A new `resolveGitDir(path)` returns `findGitDir(path)`, or the path itself
  when it has no `.git` entry and `IsGitDir` passes. `OpenWorktree` and
  `ListWorktrees` call it, so they work for all four layouts without new
  entry points. `findGitDir` itself is unchanged: `readWorktree` uses it for
  the back-link check, where a missing `.git` must keep git's wording,
  "gitdir file points to non-existent location".
- `Repository.Bare` is set when the repository is opened. `Open(gitDir)`
  sets it when `core.bare` is true and the git directory has no `commondir`
  file. `OpenWorktree(path)` also sets it when `resolveGitDir` returned the path
  itself, because only it knows the path was the git directory.

### internal/scan

- `walk` treats a folder as a repository when it holds a `.git` entry **or**
  `IsGitDir` passes, and sends both through the existing `repo(dir, rel)`.
  The walk does not enter a bare repository, its recorded worktrees are
  queued as its children, and a worktree the walk also finds is claimed
  through the existing `os.SameFile` check, so it is not sent on its own.
- The cost is one `Lstat` of `HEAD` in each folder without `.git`. `HEAD` is
  checked first, so a folder without one costs nothing more.

### internal/gitstat

- `Result` gains `Bare bool`. `inspect` opens the repository as it does now;
  when `Repository.Bare` is set, it sets `res.Bare` and returns before the
  head, status and tracking stages.
- `Result.Dirty()` is then false, which removes the false `[S?]`.
- A bare repository can fail only at the open stage. An unreadable
  repository keeps today's line, `⚠️ name ! open: <error>`, bare or not:
  bareness is read from the config that failed to open.
- A bare repository counts in "Found N git repositories": `tree.Repos()`
  and the progress counter count every repository item that is not a
  worktree. A scan root that is itself a bare repository is the root line,
  as a working tree at the root is now.

### internal/tree, internal/render, the TUI and main

- `tree.Node` gains `Worktrees() int`, the number of its worktree children.
  A bare repository's children are only its worktrees, because the walk does
  not enter it, so the count equals its records, broken ones included. No
  count is passed through `scan` or `gitstat`.
- `tree.visible` is unchanged: with the dirty-only filter, a bare repository
  is listed when one of its worktrees is.
- `render.Line` draws a bare repository as `🪵 name (N worktrees)`, with
  `(1 worktree)` for one. `IconBare = "🪵"` and the count are plain text
  segments with no style, as `IconWorktree` is.
- The legend becomes
  `✅ Clean repo | ❌ Dirty repo | 🪵 Bare repo | 🌳 Worktree | ⚠️ Directory / error`.
- The `-h` text gains `🪵 = Bare repository, with its worktrees listed under it`.
- The TUI draws the same segments; only its golden file changes. In the TUI
  the count rises as worktree results arrive and is final when the scan
  ends.

### Documentation

- README: the example tree gains a bare repository with two worktrees; "What
  a line shows" gains rows for 🪵 and `(N worktrees)`; the limitation
  "Bare repositories under the scan root are not detected" is removed; new
  limitations: `core.worktree` is not read, `safe.bareRepository` is not
  read, and 🪵 needs a Unicode 13 emoji font.
- CLAUDE.md: the `internal/git` and `internal/scan` architecture notes say
  that a folder that is itself a git directory is a repository, listed as
  bare.

## Errors and hostile input

- `HEAD` is the only file this feature adds to what is read. It is read as
  described under `IsGitDir`: bounded, regular files only, symlinks not
  followed. Its parser returns a bool, never panics, and gets a fuzz target.
- A bare repository is opened (config and format check, as for every
  repository) and inspection stops there: no refs, objects or index are
  read.
- A folder planted to pass `IsGitDir` is listed as bare and is not entered.
  Unlike a `.git` entry, which git never checks out, such a folder can come
  from a clone or an extracted archive (a regular `HEAD`, `objects/`,
  `refs/` and a symlinked `config` all check out), so `Open` now reaches
  repository-supplied files from checked-out content. The final review
  reproduced a `config` symlink to `/dev/zero` exhausting memory, so `Open`
  reads `config` (and `config.worktree`) through `OpenRegular` with a 16 MiB
  cap (`maxConfigFile`), and `commondir` as a link file (regular, at most
  4 KiB). Its `worktrees/` records pass through the existing checks: a
  regular `gitdir` file of at most 4 KiB, naming a `.git` file that must
  lead back to the same record (`os.SameFile`), so a crafted record cannot
  aim the scan at another folder.
- `extensions.worktreeConfig`: git reads `core.bare` from the git dir's
  `config.worktree` too, and `git sparse-checkout` moves a bare repository's
  `core.bare` there. `Open` follows git's `check_repository_format_gently`:
  with the extension, `config.worktree`'s `core.bare` overrides the shared
  config's, and applies in a linked worktree as well.
- `safe.bareRepository` (git 2.38) lets git refuse bare repositories found
  inside other folders, because git runs configuration and hooks from a
  repository. git-dirty runs nothing, so it does not read the setting.
- Error lines are unchanged: `⚠️ name ! open: <error>`, the `-v` warning
  `cannot list worktrees`, and `🌳 ⚠️ <id> ! worktree: ...` for a broken
  record.
- No new shared state: `IsGitDir` runs in the discovery goroutine, and
  `Repository.Bare` is set on each worker's own copy.
- A bare repository is hidden like any folder: by an excluded name or a
  `.ignore` file inside it, both checked before `walk`.

## Testing

The git CLI is the oracle. Repository fixtures are built with `git`, and
repository-level tables run in SHA-1 and SHA-256 repositories.

- **internal/git**
  - `TestIsGitDir` against `git -C <dir> rev-parse --git-dir` printing `.`,
    with `GIT_CEILING_DIRECTORIES` set to the parent and
    `safe.bareRepository=all`. Passing: `git init --bare`,
    `git clone --bare`, a working tree's `.git` folder, a detached `HEAD`, a
    `HEAD` symlink to `refs/heads/main`. Failing: a working tree's root, an empty
    folder, a folder with only `HEAD`, a FIFO `HEAD`, a garbage `HEAD`.
  - A linked worktree's admin directory: git accepts it, `IsGitDir` does
    not. Asserted separately, with the reason in a comment.
  - `TestRepositoryBare` against `git rev-parse --is-inside-work-tree`
    printing `false`: the four layouts, a working tree, a linked worktree of
    a bare repository (not bare), and a git directory with
    `core.bare = false`.
  - `ListWorktrees` on a bare folder against `git worktree list --porcelain`.
  - `FuzzValidHeadRef`, seeded with a symbolic ref, 40 and 64 hex digits
    and garbage. Fuzz runs use `-fuzzminimizetime=5s`.
- **internal/scan**: one fixture with the four layouts, a worktree elsewhere
  in the scanned folder, a worktree outside it, and a bare folder holding
  `.ignore`. Asserts that bare repositories are queued as repositories with
  their worktrees under them, that the worktree elsewhere in the folder is
  not sent on its own, that nothing inside `plain.git` is reported as a
  folder, and that the `.ignore` folder is skipped.
- **internal/gitstat**: `Inspect` on each bare layout gives `Bare` true,
  `Err` nil, and an empty head, status, upstream and default. The worktrees
  of a bare repository get normal results, checked by the existing oracles.
- **internal/render and internal/tui**: golden lines for bare repositories
  with 0, 1 and 2 worktrees, the dirty-only filter with a clean worktree
  hidden (the count still shows the total), and the legend. The TUI golden
  file includes a bare repository, and a TUI test asserts that
  `lipgloss.Width` measures `🪵` as two columns, as it does `🌳`; the golden
  file alone cannot show it, because its rows are stored with the padding
  trimmed. Golden files are rewritten with `-update` and the diff reviewed.
- **main**: plain output for a folder with a bare repository counts it in
  "Found N git repositories".
- **Beyond the tests**: rerun the 2026-11-01 probe folders with the new
  binary; `mise run all` and `mise run lint:all`; `mise run bench` for the
  extra `HEAD` check; `scripts/compare.sh`, where bare repositories are a new
  expected mismatch (the bash script never sees them) and any other
  mismatch is a bug.

## Accepted limitations

- `core.worktree` is not read: a git directory that names its working tree
  with it is listed as bare, where git would use that working tree.
- `extensions.relativeWorktrees` (git 2.48, `worktree.useRelativePaths`) is
  still refused as an unknown extension, so such a bare repository shows
  `⚠️ ! open: ...`. Unchanged and already documented.
- `IsGitDir` does not follow `commondir`; see internal/git.
- A worktree folder moved with `mv` is listed twice, as `git worktree list`
  shows it: the stale record under its repository and the folder on its own.
- In the TUI, a bare repository's count rises while its worktrees' results
  arrive.

## Out of scope

- A folder whose `.git` file names a git directory that no longer exists
  shows `! head: HEAD: reference not found`; git says "not a git repository".
  It affects every repository, not only bare ones, and is fixed afterwards on
  its own branch as a `fix(git)` commit.
- Showing the bare repository's `HEAD` branch or upstream counts: not
  chosen.
