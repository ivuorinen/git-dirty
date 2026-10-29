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
└── ✅ scratch (wip, no upstream) · vs main ↑0 ↓0
```

It is a single static binary. It reads git's on-disk formats itself (config,
refs, the index, loose objects and packs, in SHA-1 and SHA-256 repositories)
and never runs `git`, hooks, filters or `core.fsmonitor`. It makes no network
requests: ahead/behind counts are measured against the remote-tracking refs as
of your last fetch.

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
| ⚠️ | A directory without a repository, or a repository that could not be read (`! <error>`) |
| 🌳 | A linked worktree (`git worktree add`), listed under its repository, before its own ✅, ❌ or ⚠️ and the parts below. Its name is its ID under `.git/worktrees/`, which git takes from the directory name. |
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
| `↑`/`k`, `↓`/`j`, `pgup`, `pgdn`/`space` | Scroll |
| `g`/`home`, `G`/`end` | Top, bottom |
| `f` | Show only dirty or failed repositories |
| `b` | Toggle branch names |
| `r` | Rescan |
| `?` | Full help |
| `q`/`ctrl+c` | Quit |

The browser is read-only: it never changes a repository.

## Configuration

Settings are read from, lowest to highest precedence: built-in defaults, the
config file, environment variables, then flags.

The config file is `$XDG_CONFIG_HOME/git-dirty/config.json`
(`~/.config/git-dirty/config.json` when `XDG_CONFIG_HOME` is unset). Unknown
keys are an error. Every key is optional:

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

A directory is skipped when its name is in the exclude list or it contains a
file named `.ignore`. To hide a repository, add an empty `.ignore` next to its
`.git`, and list `.ignore` in your global gitignore so it is not reported as
untracked. Symbolic links are never followed, and the scan does not descend
into repositories.

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

## Known limitations

- Content filters such as `core.autocrlf` and Git LFS are not applied. A file
  whose timestamp changed without a content change can show as `M` until
  `git status` refreshes the index. The tool is read-only, so it cannot
  refresh the index itself.
- Files marked `git update-index --assume-unchanged` are still checked.
- Split-index and SHA-256 repositories are supported. A sparse index is not,
  and is shown as `! index: unsupported index extension "sdir"`; neither are
  the reftable ref format or `objects/info/alternates`, which show an error
  line too.
- The commit-graph file is not used: ahead/behind is computed from commit
  objects. The counts are the same; only very long divergences take longer.
- `[include]` and `[includeIf]` in git config files are not followed.
- Per-worktree config (`extensions.worktreeConfig`, `config.worktree`) is not
  read, and a repository with `extensions.relativeWorktrees` is refused as an
  unknown extension.
- Bare repositories under the scan root are not detected; their linked
  worktrees inside the root are listed on their own.

## Development

```sh
mise run all            # golangci-lint, race-enabled tests, govulncheck, build: the CI gates
mise run test
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

Releases are CalVer tags (`mise run release`). GoReleaser builds and signs them
in GitHub Actions.

## License

MIT. Third-party licenses are attached to each release as
`THIRD_PARTY_NOTICES.txt`.
