# git-dirty

Recursively lists the git repositories under a directory as a tree, with each
repository's branch, dirty state, and how far it is ahead of or behind its
upstream. When something other than the default branch is checked out, it also
shows how the local default branch compares with its upstream and how far the
checked-out branch has diverged from the default branch.

```text
⚠️ /home/u/Code
├── ⚠️ ivuorinen
│   ├── ❌ git-dirty [M?] (feature/tree ↑2) · main ↓4 · vs main ↑5 ↓1
│   │   ├── 🌳 ✅ feat (feat, no upstream) · vs main ↑1 ↓0
│   │   └── 🌳 ⚠️ old ! worktree: gitdir file points to non-existent location
│   ├── ✅ dotfiles ↓3
│   ├── ❌ gh-history ↑1
│   └── ⚠️ broken ! index: unknown extension
├── 🪵 plain.git (2 worktrees)
│   ├── 🌳 ❌ plain-feat [M] (feat, no upstream) · vs main ↑1 ↓0
│   └── 🌳 ✅ plain-main
├── ✅ scratch (wip, no upstream) · vs main ↑0 ↓0
└── 📁 work
    ├── ✅ api
    └── ✅ web ↓2
```

It is a single static binary. It reads git's on-disk formats itself (config,
refs, the index, loose objects and packs, in SHA-1 and SHA-256 repositories),
and the scan never runs `git`, hooks, filters or `core.fsmonitor`. The scan
makes no network requests: ahead/behind counts are measured against the
remote-tracking refs as of your last fetch. The browser's fetch and pull keys
are the exception: they run `git` in the selected repository ([Actions](#actions)).

On a terminal it opens an interactive browser. When its output is piped, or
with `--plain`, it prints the report.

## Install

Download a binary from the
[releases](https://github.com/ivuorinen/git-dirty/releases). Each release's
checksums file is signed with cosign.

To build from source:

```sh
mise install        # Go (from go.mod) and the dev tools
mise run build      # ./git-dirty
```

## Usage

```text
git-dirty [OPTIONS] [DIRECTORY]
```

`DIRECTORY` defaults to `~/Code`.

| Flag | Meaning |
| --- | --- |
| `-h` | Show help and exit |
| `-v` | Verbose: list directories without repositories too, and log skipped directories and full error messages to stderr |
| `-a` | Check stash and untracked files (`$` and `?`) |
| `-d NUM` | With `-v`, list directories without repositories only down to this depth (default 5) |
| `-m NUM` | Maximum recursion depth (default 15) |
| `-e PATTERNS` | Extra directory names to skip, comma separated; repeatable |
| `-c` | Toggle colored output |
| `-t` | Toggle path truncation: when off, each line shows its full path |
| `-b` | Toggle branch names (counts are always shown) |
| `-p` | Accepted for compatibility; scanning is always parallel |
| `--plain` | Print the report instead of opening the interactive browser |
| `--version` | Print the version and exit |

Short flags can be bundled as with the old script (`-vac`, `-d3`), and flags
may follow the directory.

Exit status: 0 on success, 1 on an error (bad directory or configuration), 2 on
a usage error, 130 when interrupted.

### What a line shows

`<icon> <name> [<letters>] <branch> · <default> ↑x ↓y · vs <default> ↑p ↓q`

| Part | Meaning |
| --- | --- |
| ✅ / ❌ | Clean / dirty. Dirty means any status letter is set, or there are commits not pushed to the upstream. Being behind does not make a repository dirty. |
| 📁 | A directory that is not a repository, with no dirty or unreadable repository or worktree anywhere below it. Repositories that are only behind their upstream do not change it. |
| ⚠️ | A repository that could not be read (`! <error>`), or a directory with a dirty or unreadable repository or worktree below it: the directories the `d` view keeps |
| 🌳 | A linked worktree (`git worktree add`), listed under its repository, before its own ✅, ❌ or ⚠️ and the parts below. Its name is its ID under `.git/worktrees/` (`<bare>/worktrees/` for a bare repository), which git takes from the directory name. |
| 🪵 | A bare repository: a git directory with no working tree, such as one made by `git clone --bare`, or a `.git` whose config sets `core.bare = true`. It has no status letters, branch or ✅/❌; its linked worktrees are listed under it, marked 🌳. |
| `(N worktrees)` | After 🪵, how many linked worktrees the bare repository records, stale ones included. It stays the same when `d` hides some. When they cannot be listed, `! worktrees: <error>` replaces it. |
| `M` | Worktree differs from the index (`git diff --quiet` would fail) |
| `S` | Index differs from HEAD (`git diff --cached --quiet` would fail) |
| `?` | Untracked files that are not ignored (on by default; `GIT_DIRTY_CHECK_UNTRACKED=0` turns it off) |
| `$` | A stash exists (with `-a`) |
| `(name ↑a ↓b)` | Current branch and its commits ahead of / behind its upstream. Zero counts are omitted. `no upstream` and `upstream gone` explain missing counts. On a detached HEAD, the exact tag or the short hash replaces the name. |
| bare `↑a ↓b` | The same counts for a branch in the main-branch list, whose name is hidden |
| `· main ↓4` | The local default branch against its own upstream. Shown only when not on the default branch and a count is non-zero. |
| `· vs main ↑5 ↓1` | The checked-out commit against the default branch (`origin/main` when it exists, else the local `main`). Always shown, with both counts, when not on the default branch. |

The default branch is what `refs/remotes/<remote>/HEAD` points at, using the
current branch's remote or `origin`. Without that ref, it is the first branch
in the main-branch list that exists locally.

### Interactive browser

| Key | Action |
| --- | --- |
| `↑`/`k`, `↓`/`j` | Move the cursor one line |
| `pgup`, `pgdn`/`space` | Move the cursor one page |
| `g`/`home`, `G`/`end` | First, last line |
| `←`/`h`, `→`/`l` | Scroll sideways |
| `d` | Show only dirty or failed repositories |
| `b` | Toggle branch names |
| `r` | Rescan |
| `f` | `git fetch --all` in the selected repository |
| `F` | `git fetch --all --prune` in the selected repository, after y/n |
| `p` | `git pull --ff-only --autostash` in the selected repository |
| `P` | `git pull --rebase=true` in the selected repository, after y/n |
| `m` | [Mass actions](#mass-actions): one of the four on every listed repository |
| `s` | [Settings](#settings-page): edit the config file |
| `?` | Full help |
| `q` | Quit; asks first while an action runs |
| `ctrl+c` | Quit at once |

### Actions

`f`, `F`, `p` and `P` run `git` in the repository under the cursor. The
uppercase key is the destructive variant of the lowercase one and asks first.

| Key | Runs | Runs on |
| --- | --- | --- |
| `f` | `git fetch --all` | A repository, a worktree or a bare repository |
| `F` | `git fetch --all --prune`: also deletes remote-tracking branches the remote no longer has | The same |
| `p` | `git pull --ff-only --autostash`: fast-forwards only, stashing uncommitted changes and reapplying them | A working tree on a branch with an upstream that exists |
| `P` | `git pull --rebase=true`: rebases the branch's own commits onto the upstream | The same |

- One action runs at a time, never during a scan, and a rescan follows each
  one. The status line shows `ok` or git's last line. On any other line, the
  status line says why nothing ran, such as `pull: HEAD is detached`.
- git runs in the repository's directory, with no terminal, stdin on the null
  device, `GIT_TERMINAL_PROMPT=0`, and without git's repository-local
  variables (`GIT_DIR`, `GIT_WORK_TREE` and the others
  `git rev-parse --local-env-vars` lists). Nothing can be typed: a remote
  needs ssh-agent or a credential helper. A key passphrase, an unknown host
  key or a password prompt makes the action fail with git's message.
- It is git, run as you would run it in that directory: the repository's
  hooks, `core.fsmonitor`, `core.sshCommand` and credential helpers run, and
  `safe.directory` and `safe.bareRepository` apply. Run actions only in
  repositories you trust.
- `p` on a branch that has diverged from its upstream fails and changes
  nothing. `P` with uncommitted changes fails unless `rebase.autoStash` is
  set. An autostash that conflicts when it is reapplied leaves the changes in
  `git stash list`.
- `q` asks first while an action runs, because quitting interrupts git: an
  interrupted `P` can leave a rebase in progress (`git rebase --abort`).

### Mass actions

`m` opens a menu of the same four actions, each with how many listed
repositories it would run on. What is listed is what runs: with `d` on, only
the dirty and unreadable repositories. A fetch skips a worktree listed under
its repository, because the repository's fetch updates the worktree's
remote-tracking branches; a pull skips every repository `p` would refuse.

Choosing an action opens its warning page: what the action changes, how many
repositories it runs on, how many of them are bare, and how many are skipped
and why. `y` starts the run and `esc` goes back.

The run goes one repository at a time, in the order listed: worktrees of one
repository share its refs and lock files. A grid shows one cell per
repository: `□` waiting, `▣` running, `■` done, and a failure as a red `■`,
or `✗` without color. `esc` stops the run after the running repository. When
the run ends, each failure is listed with git's last line, and `enter` or
`esc` goes back to the tree and rescans. Without git on `PATH`, the run ends
at its first repository.

## Configuration

Settings are read from, lowest to highest precedence: built-in defaults, the
config file, environment variables, then flags.

The config file is `$XDG_CONFIG_HOME/git-dirty/config.json`
(`~/.config/git-dirty/config.json` when `XDG_CONFIG_HOME` is unset), or the
file `--config FILE` names. A missing file means the defaults; `-h` shows the
file in use. Unknown keys are an error. Every key is optional:

```json
{
  "dir": "~/Code",
  "depth": 5,
  "maxDepth": 15,
  "checkStash": false,
  "checkUntracked": true,
  "exclude": ["node_modules", "vendor", ".cache", "build", "dist", ".tests", ".test"],
  "color": true,
  "truncate": true,
  "showBranch": true,
  "mainBranches": ["main", "master", "trunk"],
  "interactive": true,
  "verbose": false
}
```

| Environment variable | Key | Notes |
| --- | --- | --- |
| `GIT_DIRTY_DIR` | `dir` | |
| `GIT_DIRTY_DEPTH` | `depth` | |
| `GIT_DIRTY_MAXDEPTH` | `maxDepth` | |
| `GIT_DIRTY_CHECK_STASH` | `checkStash` | `0`/`1`/`true`/`false` |
| `GIT_DIRTY_CHECK_UNTRACKED` | `checkUntracked` | |
| `GIT_DIRTY_EXCLUDE` | `exclude` | space separated; replaces the list (`-e` appends) |
| `GIT_DIRTY_COLOR` | `color` | |
| `GIT_DIRTY_TRUNCATE` | `truncate` | |
| `GIT_DIRTY_SHOW_BRANCH` | `showBranch` | |
| `GIT_DIRTY_MAIN_BRANCHES` | `mainBranches` | space separated |
| `VERBOSE` | `verbose` | |
| `NO_COLOR` | | any non-empty value turns color off |

`interactive` has no environment variable; `--plain` turns it off.

### Settings page

`s` in the browser opens the config file's settings as a form: every key
except `interactive`, which only `--plain` turns off, so saving it off would
leave editing the file as the only way back. The form shows the file's
values, not the environment's or the flags'; a row that one of them
overrides also shows `overridden:` and the value in effect.

`↑`/`↓` choose a row and `enter` toggles a true/false row or edits the
others: type, paste or backspace, then `enter` keeps the value and `esc`
drops it. `exclude` and `mainBranches` are comma separated, as `-e` takes
them. `ctrl+s` checks the settings as startup does (a missing `dir` is an
error), writes the file and rescans with them; on an error nothing is
written. `esc` leaves the page and drops unsaved edits.

A save writes only the keys whose values differ from the defaults, so a
later change of a default reaches you; a hand-written key equal to its
default is dropped. A symlinked config file stays a symlink and its target
is replaced. The file keeps its mode, and a new one is created `0600`.

A directory is skipped when its name is in the exclude list or it contains a
file named `.ignore`. To hide a repository, add an empty `.ignore` next to its
`.git` (inside the repository's folder, for a bare repository), and list
`.ignore` in your global gitignore so it is not reported as untracked.
Symbolic links are never followed, and the scan does not descend into
repositories.

A repository's linked worktrees come from its own records, not from the scan,
so they are listed under it wherever they are on disk: the exclude list,
`.ignore` and the depth limits do not apply to them. A linked worktree that the
scan finds itself stays at its own place only when its repository is not in
the scan. A worktree whose directory was deleted shows ⚠️ with git's wording,
`gitdir file points to non-existent location`, until `git worktree prune`
removes it. Worktrees are not counted in "Found N git repositories".

Ignore rules follow git's: `core.excludesFile` (default
`~/.config/git/ignore`), `.git/info/exclude`, and every `.gitignore`.

## Differences from the bash script

| Bash script | git-dirty |
| --- | --- |
| Sources `~/.config/git-dirty/config` as shell code | Reads `~/.config/git-dirty/config.json`; the shell file is not read |
| The sourced config file overrides environment variables | Environment variables override the config file |
| Indented list | Tree drawn with `├──`, `└──` and `│` |
| `↑` letter for unpushed commits | Explicit `↑N ↓N` counts, plus the default-branch comparisons |
| `-p` needs GNU parallel | `-p` is accepted and ignored; work is always concurrent |
| `-d` is documented but not used | `-d` limits the depth of non-repository directories under `-v` |
| `-t` has no visible effect | `-t` off prints full paths |
| Stash check reads the loose `refs/stash` file only | Packed refs are found too |
| A linked worktree is listed only where the scan finds it, as a repository | Every linked worktree is listed under its repository, marked 🌳 |
| A bare repository's folder is walked into, and its worktrees are listed on their own | A bare repository is listed with 🪵, its worktrees under it |
| Every directory is marked ⚠️ | A directory is marked 📁, or ⚠️ when a repository below it is dirty or unreadable |

## Known limitations

- Content filters such as `core.autocrlf` and Git LFS are not applied. A file
  whose timestamp changed without a content change can show as `M` until
  `git status` refreshes the index. The scan does not write to repositories,
  so it cannot refresh the index itself.
- Files marked `git update-index --assume-unchanged` are still checked.
- Split-index and SHA-256 repositories are supported. A sparse index is not,
  and is shown as `! index: unsupported index extension "sdir"`; neither are
  the reftable ref format or `objects/info/alternates`, which show an error
  line too.
- The commit-graph file is not used: ahead/behind is computed from commit
  objects. The counts are the same; only very long divergences take longer.
- `[include]` and `[includeIf]` in git config files are not followed.
- Per-worktree config (`extensions.worktreeConfig`, `config.worktree`) is read
  for `core.bare` only, which decides whether a repository is listed as bare
  (🪵); its other settings are not read. A repository with
  `extensions.relativeWorktrees` is refused as an unknown extension.
- `core.worktree` is not read: a git directory that names its working tree
  with it is listed as bare (🪵), where git would use that working tree.
- A git directory made with `--separate-git-dir` (`git init` or `git clone`)
  is listed as bare (🪵) when the scan reaches it, and its working tree is
  listed on its own: the git directory does not record where its working
  tree is, so the two cannot be joined.
- `safe.bareRepository` is not read by the scan. git uses it to refuse bare
  repositories found inside other folders, because git runs configuration and
  hooks from a repository; the scan runs nothing, and lists every bare
  repository it finds. The fetch keys run git, which applies it.
- 🪵 (U+1FAB5) is a Unicode 13 emoji. A terminal whose emoji font predates it
  prints a box in its place.
- The mass-action grid's `□ ▣ ■` are East Asian ambiguous width: a terminal
  that draws them two columns wide misaligns the grid.

## Development

```sh
mise run all            # golangci-lint, race-enabled tests, tests with the oldest git, govulncheck, build: the CI gates
mise run test           # with the newest git; test-oldest-git with the oldest supported (2.39.2)
mise run lint:fix       # golangci-lint with its fixes applied
mise run hooks:install  # pre-commit hooks (prek): linters for Go, Markdown, YAML, workflows, shell
mise run lint:all       # every pre-commit hook on every file
mise run bench          # hyperfine against the bash script (warm cache)
scripts/compare.sh      # repository-by-repository comparison with the bash script
```

The bash script git-dirty replaces is kept as `scripts/git-dirty.sh`, the
reference that `bench` and `compare.sh` run.

The tests build real repositories with the `git` binary and use git's own
answers (`git diff --quiet`, `git rev-list --left-right --count`, and others)
as the expected values, in both SHA-1 and SHA-256 repositories.

The readers in `internal/git` have fuzz targets. Run one with a short
minimization limit, otherwise new inputs are minimized for up to a minute
and the run looks stalled:

```sh
go test ./internal/git/ -run '^$' -fuzz '^FuzzIndex$' -fuzztime=60s -fuzzminimizetime=5s
```

## Releasing

Releases use Semantic Versioning tags (`v0.2.0`), the only version form
`go install` understands. Pushing a tag is the release: the release workflow
builds the binaries with GoReleaser, signs the checksums file with cosign, and
publishes the GitHub release.

```sh
mise run release
```

The task requires a clean `main` that is the same commit as `origin/main`. It
fetches `origin` first and stops if either is not true.

[svu](https://github.com/caarlos0/svu) picks the version from the commit
messages since the last tag: `fix:` bumps the patch version, `feat:` the minor
version, and a breaking change (`feat!:`, `fix!:` or a `BREAKING CHANGE:`
footer) the minor version while the version is 0.x. When no commit since the
last tag calls for a release, svu prints the current tag, and the task stops
with "Nothing to release" before it tags anything.

Renovate's commits follow the same rule. An update to a Go module or to the Go
toolchain, both compiled into the binary, is a `fix(deps)` commit and releases
a patch version. Updates to GitHub Actions and to development tools are
`chore` commits and release nothing. Merging an update does not release it;
`mise run release` does.

The release notes list the commits by type, under Features, Bug fixes,
Refactoring, Build, CI and Chores; commits of any other type, and merge
commits, go under Others. `docs` and `test` commits are left out.

The first release had a CalVer tag (`2026.10.0`), which svu ignores. With no
SemVer tag to start from, svu stops the task, so the first one is made once by
hand:

```sh
git switch main && git pull --ff-only
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```

## License

MIT. Third-party licenses are attached to each release as
`THIRD_PARTY_NOTICES.txt`.
