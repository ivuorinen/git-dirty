# Bare Repositories Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended)
> or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** List a bare repository as `🪵 name (N worktrees)` with its linked
worktrees under it, and stop inspecting a bare repository as a working tree.

**Architecture:** `internal/git` learns git's "is a git directory" test and
marks a repository bare when it is opened. `internal/scan` treats a folder
that is itself a git directory as a repository. `gitstat.Inspect` stops after
the open stage for a bare repository, and `render` draws it from the tree
node's worktree children.

**Tech Stack:** Go (standard library only in `internal/git`), bubbletea v2 and
lipgloss v2 in the TUI, the git CLI as the test oracle, mise and prek.

**Spec:** `docs/plans/2026-11-01-bare-repositories-design.md`

## Global Constraints

- `internal/git` imports the standard library only (depguard).
- Never execute `git` or anything else at runtime; tests may, with the
  existing `//nolint:gosec // G204: ...` comment.
- Everything `internal/git` reads may come from a hostile clone: `HEAD` is
  read through `OpenRegular`, at most 255 bytes, and a symlinked `HEAD` is
  checked by its `Readlink` target only, never followed. A decoder never
  panics and gets a fuzz target.
- The git CLI is the oracle. Repository-level tests run in SHA-1 and SHA-256
  (`formats` in `internal/git`, `forEachFormat` in `internal/gitstat`).
- golangci-lint findings are fixed, not suppressed.
- Bare line, exact: `🪵 name (N worktrees)`, `(1 worktree)` for one,
  `(0 worktrees)` for none. `IconBare = "🪵"` (U+1FAB5).
- Legend, exact:
  `Legend: ✅ Clean repo | ❌ Dirty repo | 🪵 Bare repo | 🌳 Worktree | ⚠️ Directory / error`.
- Help line, exact: `🪵 = Bare repository, with its worktrees listed under it`.
- Golden files are stored without trailing spaces; rewrite with `-update` and
  review the diff.
- Fuzzing: pass `-fuzzminimizetime=5s`.
- Commits only when the user says to commit. Stage the listed paths, never
  `git add -A`, check `git status --short` and `git diff --cached` first, no
  `--no-verify`, no attribution trailers.

## Review Focus

- A worktree created from inside the bare folder
  (`git -C plain.git worktree add inside`) has its files inside the folder
  the walk does not enter; it must still be listed once, under the bare
  repository. Tested in Task 2 and Task 4.
- The scan root is itself a bare repository (`git-dirty plain.git`): the
  root line is the bare line and its worktrees are listed under it. Tested in
  Task 4.
- A bare repository excluded by name: its worktrees that the walk finds are
  listed on their own, as for an excluded working tree. Tested in Task 4.
- A `HEAD` file larger than 255 bytes that starts with a valid ref: accepted,
  as git's `validate_headref` accepts it, without reading the rest. Tested in
  Task 1.
- `-v` lists no folder inside a bare repository (`objects/`, `refs/`, ...).
  Tested in Task 4.

## Fixture layouts

Tasks 2 to 4 build the same layouts, each with its package's own git helper
(`run` in `internal/git`, `git` in `internal/gitstat`, `gitIn` in
`internal/scan`). `<seed>` is a repository with one commit on `main`; `R` is
the fixture root.

| Name | Commands |
| --- | --- |
| `plain.git` | `git clone -q --bare <seed> R/plain.git`; `git -C R/plain.git worktree add -q --detach R/plain-main`; `git -C R/plain.git worktree add -q --detach inside` (creates `R/plain.git/inside`) |
| `proj` | `git clone -q --bare <seed> R/proj/.bare`; write `R/proj/.git` as `gitdir: ./.bare\n`; `git -C R/proj worktree add -q --detach main` |
| `dotgit` | `git clone -q --bare <seed> R/dotgit/.git`; `git -C R/dotgit worktree add -q --detach main` |
| `lonely.git` | `git clone -q --bare <seed> R/lonely.git` |
| `nobare.git` | `git init -q --bare R/nobare.git`; `git -C R/nobare.git config core.bare false` |

---

### Task 1: `IsGitDir` in internal/git

**Files:**

- Modify: `internal/git/repository.go`
- Create: `internal/git/repository_test.go`
- Modify: `internal/git/repository_unix_test.go`
- Modify: `internal/git/fuzz_test.go`

**Interfaces:**

- Produces: `func IsGitDir(dir string) bool`; unexported
  `func validHeadRef(b []byte) bool` and `const maxHeadRef = 255`.

- [ ] **Step 1: Write the failing tests**

In `repository_test.go`, `TestIsGitDir` runs per format and per case, with a
table of `{name, dir, want}`. For every case it checks the oracle against
`want` (so the oracle cannot drift silently), then `IsGitDir(dir)` against
`want`:

```go
// gitSaysGitDir is git's answer: rev-parse --git-dir prints "." only when
// dir itself is a git directory. The ceiling stops git looking above dir.
func gitSaysGitDir(t *testing.T, dir string) bool {
    t.Helper()
    t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
    out, code := runCode(t, dir, nil, "-c", "safe.bareRepository=all", "rev-parse", "--git-dir")

    return code == 0 && out == "."
}
```

| Case | Built with | want |
| --- | --- | --- |
| `init --bare` | `git init -q --bare --object-format=<f>` | true |
| `clone --bare` | `git clone -q --bare` of a repository with one commit | true |
| working tree `.git` folder | `newRepo(t, f)` + `/.git` | true |
| detached `HEAD` | `clone --bare`, then `git update-ref --no-deref HEAD <commit id>` | true |
| `HEAD` symlink | `init --bare`, `HEAD` replaced by `os.Symlink("refs/heads/main", HEAD)`; `t.Skip` if symlinks are unavailable | true |
| `HEAD` over 255 bytes | `init --bare`, `HEAD` = `ref: refs/heads/main\n` followed by 1 MiB of `x` | true |
| working tree root | `newRepo(t, f)` | false |
| empty folder | `t.TempDir()` | false |
| only `HEAD` | folder with `HEAD` = `ref: refs/heads/main\n` | false |
| garbage `HEAD` | `init --bare`, `HEAD` = `garbage\n` | false |
| `HEAD` symlink outside `refs/` | `init --bare`, `HEAD` replaced by a symlink to `../x` | false |

`TestIsGitDir_WorktreeAdminDir`: a repository with one linked worktree;
`gitSaysGitDir(admin)` is true and `IsGitDir(admin)` is false, where `admin`
is `.git/worktrees/<id>`. A comment says why: `IsGitDir` does not follow
`commondir` (spec, internal/git).

In `repository_unix_test.go`, `TestIsGitDir_FIFOHead`: `init --bare`, replace
`HEAD` with `syscall.Mkfifo`; `IsGitDir` returns false without blocking. No
oracle: git's `open()` would block on the FIFO.

In `fuzz_test.go`, `FuzzValidHeadRef`, seeded with
`ref: refs/heads/main\n`, 40 and 64 hex digits, `ref: HEAD\n` and `garbage`.
It fails when `validHeadRef(data)` is true but `data` neither starts with
`ref:` nor has 40 leading hex digits.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/git/ -run 'TestIsGitDir|FuzzValidHeadRef'`
Expected: FAIL to compile, `undefined: IsGitDir` and `undefined: validHeadRef`.

- [ ] **Step 3: Implement `IsGitDir(dir string) bool` and `validHeadRef(b []byte) bool` in `repository.go`**

`IsGitDir` checks `HEAD` first, so a folder without one costs one `Lstat`:
a symlink passes when its `os.Readlink` target starts with `refs/`; a regular
file passes when `validHeadRef` accepts its first `maxHeadRef` bytes, read
through `OpenRegular` and `io.LimitReader`; anything else, and any error,
fails. Then `objects` and `refs` must each pass `os.Stat` with `IsDir`.
`validHeadRef`: `ref:`, then any of `" \t\n\r"`, then `refs/`; or at least 40
leading hex digits, upper or lower case, as `get_oid_hex` reads.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/git/ -run 'TestIsGitDir|FuzzValidHeadRef'`
Expected: PASS.
Run: `go test ./internal/git/ -run '^$' -fuzz FuzzValidHeadRef -fuzztime 30s -fuzzminimizetime=5s`
Expected: PASS, no new crashers in `testdata/fuzz`.

- [ ] **Step 5: Commit, when the user says to**

```bash
git add internal/git/repository.go internal/git/repository_test.go internal/git/repository_unix_test.go internal/git/fuzz_test.go
git commit -m "feat(git): recognize a folder that is itself a git directory"
```

### Task 2: bare repositories in internal/git

**Files:**

- Modify: `internal/git/repository.go` (`Repository`, `Open`, `OpenWorktree`,
  new `resolveGitDir`)
- Modify: `internal/git/worktree.go` (`ListWorktrees`)
- Modify: `internal/git/repository_test.go`, `internal/git/worktree_test.go`

**Interfaces:**

- Consumes: `IsGitDir` (Task 1).
- Produces: `Repository.Bare bool`; `OpenWorktree(path)` and
  `ListWorktrees(path)` accept a path that is itself a git directory;
  unexported `func resolveGitDir(path string) (gitDir string, self bool, err error)`.

- [ ] **Step 1: Write the failing tests**

`TestRepositoryBare`, per format, over the fixture layouts plus a working tree
(`newRepo`) and the linked worktree `R/plain-main`. The oracle and `want`:

```go
// gitSaysBare: git cannot run `git status` here.
func gitSaysBare(t *testing.T, path string) bool {
    t.Helper()
    t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(path))
    out, code := runCode(t, path, nil, "rev-parse", "--is-inside-work-tree")

    return code == 0 && out == "false"
}
```

| Path | want |
| --- | --- |
| `R/plain.git`, `R/lonely.git`, `R/nobare.git` | true |
| `R/proj`, `R/dotgit` | true |
| working tree | false |
| `R/plain-main` (worktree of a bare repository) | false |

Each case asserts `gitSaysBare(path) == want` and
`openRepo(t, path).Bare == want`.

`TestListWorktrees_BareRepository`, per format: `ListWorktrees(R/plain.git)`
returns `linked == false`, no error, and the worktrees of
`porcelain(t, R/plain.git)` (`plain-main` and `inside`), each checked with
`checkListed`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/git/ -run 'TestRepositoryBare|TestListWorktrees_BareRepository'`
Expected: FAIL to compile, `r.Bare undefined`.

- [ ] **Step 3: Implement**

- `Repository` gains `Bare bool // no working tree: the git dir itself, or core.bare without commondir`.
- `Open(gitDir)` sets `Bare` when `Config.Bool("core", "", "bare", false)` is
  true and the `commondir` file was absent.
- `resolveGitDir(path)` returns `findGitDir(path)`; when that fails with
  `fs.ErrNotExist` and `IsGitDir(path)` passes, it returns `path, true, nil`.
  `findGitDir` itself is unchanged: `readWorktree` keeps using it.
- `OpenWorktree` and `ListWorktrees` call `resolveGitDir`; `OpenWorktree`
  sets `Bare = true` when `self` is true. Update their doc comments.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/git/`
Expected: PASS, including the existing worktree tests (the back-link error
wording is unchanged).

- [ ] **Step 5: Commit, when the user says to**

```bash
git add internal/git/repository.go internal/git/worktree.go internal/git/repository_test.go internal/git/worktree_test.go
git commit -m "feat(git): open bare repositories and list their worktrees"
```

### Task 3: `Result.Bare` in internal/gitstat

**Files:**

- Modify: `internal/gitstat/inspect.go`
- Modify: `internal/gitstat/inspect_test.go`

**Interfaces:**

- Consumes: `Repository.Bare` (Task 2), reached as `r.repo.Bare` on `*Repo`.
- Produces: `Result.Bare bool`.

- [ ] **Step 1: Write the failing test**

`TestInspect_Bare` in `forEachFormat`, on the fixture layouts built with
`newRepo(t)` as `<seed>`. For `R/plain.git`, `R/proj`, `R/dotgit` and
`R/lonely.git`, `res := Inspect(context.Background(), path, inspectOpts(t))`:

```go
if !res.Bare || res.Err != nil || res.Dirty() || res.Status != (Status{}) ||
    res.Head.Label != "" || res.Upstream != nil || res.Default != nil {
    t.Errorf("%s: %+v, want a bare result and nothing inspected", name, res)
}
```

For `R/plain-main`, after `write(t, wt, "a.txt", "changed\n")`: `res.Bare` is
false, `res.Err` is nil, and `res.Status.Modified` matches
`git diff --quiet` exiting 1 (`gitCode`).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/gitstat/ -run TestInspect_Bare`
Expected: FAIL to compile, `res.Bare undefined`.

- [ ] **Step 3: Implement**

`Result` gains `Bare bool // no working tree; nothing past the open stage is inspected`.
In `inspect`, right after the successful `Open` (the deferred `Close` stays):
when `r.repo.Bare`, set `res.Bare = true` and return nil.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/gitstat/`
Expected: PASS.

- [ ] **Step 5: Commit, when the user says to**

```bash
git add internal/gitstat/inspect.go internal/gitstat/inspect_test.go
git commit -m "feat(gitstat): stop at the open stage for a bare repository"
```

### Task 4: bare repositories in the walk (internal/scan)

**Files:**

- Modify: `internal/scan/scan.go` (`walk`, its doc comment, the package doc)
- Modify: `internal/scan/scan_test.go`

**Interfaces:**

- Consumes: `gitrepo.IsGitDir` (Task 1); `ListWorktrees` on a git directory
  (Task 2); `Result.Bare` (Task 3).

- [ ] **Step 1: Write the failing test**

`bareFixture(t) (base string)` builds the fixture layouts in `base`, plus a
worktree of `plain.git` outside `base` (`<t.TempDir()>/away`) and
`ignored.git` (`clone --bare`, then an empty `ignored.git/.ignore`).

```go
var underBare = map[string]bool{
    "plain.git": false, "plain.git/plain-main": true, "plain.git/inside": true,
    "plain.git/away": true, "proj": false, "proj/main": true,
    "dotgit": false, "dotgit/main": true, "lonely.git": false,
}
```

`TestRun_BareRepositories`, table-driven as `TestRun_Worktrees`, with
`checkRepos(t, repoItems(t, opts), want)`:

| Case | Root | Exclude | want |
| --- | --- | --- | --- |
| base | `base` | | `underBare` |
| root is the bare repository | `base/plain.git` | | `{"": false, "plain-main": true, "inside": true, "away": true}` |
| bare repository excluded | `base` | `plain.git` | `underBare` without the `plain.git` keys, plus `"plain-main": false` |

In the base case it also asserts:

- `Result.Bare` is true for `plain.git`, `proj`, `dotgit`, `lonely.git` and
  false for every worktree item;
- with `collect(t, baseOpts(base))`, no entry of `dirs` starts with
  `plain.git/` and `warnings` contains `ignored.git: skipped: contains .ignore`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/scan/ -run TestRun_BareRepositories`
Expected: FAIL: `unexpected item at "plain-main"` and `no item at "plain.git"`
(today the walk enters `plain.git`), and `proj`, `dotgit` not `Bare`.

- [ ] **Step 3: Implement**

In `walk`, a folder is a repository when `os.Lstat(dir/.git)` succeeds or
`gitrepo.IsGitDir(dir)` passes; both go to `d.repo(dir, rel)`. Update the
comments that say a repository is "a directory containing a .git entry".

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/scan/`
Expected: PASS, including `TestRun_Worktrees` unchanged.

- [ ] **Step 5: Commit, when the user says to**

```bash
git add internal/scan/scan.go internal/scan/scan_test.go
git commit -m "feat(scan): treat a folder that is a git directory as a repository"
```

### Task 5: the bare line in tree, render and the TUI

**Files:**

- Modify: `internal/tree/tree.go` (`Node.Worktrees`)
- Modify: `internal/render/render.go` (`IconBare`, `Line`, `Header`)
- Modify: `internal/render/render_test.go`, `internal/render/testdata/*.golden`
- Modify: `internal/tui/model_test.go`, `internal/tui/testdata/view-80x24.golden`

**Interfaces:**

- Consumes: `Result.Bare` (Task 3).
- Produces: `func (n *Node) Worktrees() int`; `render.IconBare = "🪵"`.

- [ ] **Step 1: Write the failing tests**

In `render_test.go`, add `bare(rel string) scan.Item`, which returns
`repo(rel, gitstat.Result{Bare: true})`, and add to `fixture()`:

```go
bare("ivuorinen/plain.git"),
worktree("ivuorinen/plain.git/plain-feat", gitstat.Result{
    Head: onBranch("feat"), Status: gitstat.Status{Modified: true},
}),
worktree("ivuorinen/plain.git/plain-main", gitstat.Result{Head: onBranch(mainBranch)}),
bare("lonely.git"),
bare("single.git"),
worktree("single.git/wt", gitstat.Result{Head: onBranch(mainBranch)}),
```

In `model_test.go`, add to `results()` a bare repository `bare.git` and its
worktree `bare.git/wt` (`Worktree: true`, on `main`), and:

```go
func TestIconBareWidth(t *testing.T) {
    if got, want := lipgloss.Width(render.IconBare), lipgloss.Width(render.IconWorktree); got != want || want != 2 {
        t.Errorf("IconBare is %d columns, IconWorktree %d; want both 2", got, want)
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/render/ ./internal/tui/`
Expected: FAIL to compile (`undefined: render.IconBare`). The golden
mismatches show once it compiles.

- [ ] **Step 3: Implement**

- `Node.Worktrees()` counts the children with `Worktree` set.
- `IconBare = "🪵"` beside the other icons; the icons comment names it.
- `Line`: when `n.Repo.Bare`, return the prefix, then plain-text segments
  `IconBare + " " + name` and `" (" + count + ")"`, where count is
  `N worktrees`, or `1 worktree` for one. No status, branch, default or error
  segments.
- `Header`: the legend from Global Constraints.

- [ ] **Step 4: Rewrite the golden files and review the diff**

Run: `go test ./internal/render/ ./internal/tui/ -update && git diff -- internal/render/testdata internal/tui/testdata`
Expected in `default.golden`, besides the new legend:

```text
├── 🪵 lonely.git (0 worktrees)
│   └── 🪵 plain.git (2 worktrees)
│       ├── 🌳 ❌ plain-feat [M] (feat, no upstream)
│       └── 🌳 ✅ plain-main
├── 🪵 single.git (1 worktree)
│   └── 🌳 ✅ wt
Found 12 git repositories in 0.21s
```

`plain.git` sorts last under `ivuorinen`, so `git-dirty` becomes `├──` and
its worktrees' indent becomes `│   │`. `dirty-only.golden` keeps `🪵 plain.git (2 worktrees)` with only
`plain-feat` under it, and drops `lonely.git` and `single.git`. The TUI
golden gains `🪵 bare.git (1 worktree)` and `🌳 ✅ wt`.

Run: `go test ./internal/tree/ ./internal/render/ ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Commit, when the user says to**

```bash
git add internal/tree/tree.go internal/render/render.go internal/render/render_test.go internal/render/testdata internal/tui/model_test.go internal/tui/testdata
git commit -m "feat(render): list a bare repository with 🪵 and its worktree count"
```

### Task 6: help text and the end-to-end test (main)

**Files:**

- Modify: `main.go` (help text)
- Modify: `main_test.go`, `testdata/plain.golden`

**Interfaces:**

- Consumes: Tasks 1 to 5.

- [ ] **Step 1: Write the failing test**

In `TestRunPlain_Golden`, after `alpha` is created:

```go
// A bare clone with a worktree the walk also finds: listed once, under it.
gitIn(t, root, "clone", "-q", "--bare", alpha, filepath.Join(root, "delta.git"))
gitIn(t, filepath.Join(root, "delta.git"), "worktree", "add", "-q", "-b", "delta-side",
    filepath.Join(root, "delta-wt"))
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run TestRunPlain_Golden`
Expected: FAIL: a golden mismatch showing `delta.git` and `delta-wt`.

- [ ] **Step 3: Implement**

Add the help line from Global Constraints after the `🌳` line in `main.go`.

- [ ] **Step 4: Rewrite the golden file and review the diff**

Run: `go test . -run TestRunPlain_Golden -update && git diff -- testdata/plain.golden`
Expected: the new legend, `🪵 delta.git (1 worktree)` with
`🌳 ✅ delta-wt (delta-side, no upstream)` under it, followed by the same
default-branch part as `alpha-side`'s line; no standalone `delta-wt` line;
and "Found N" one higher than before.
Run: `go test .`
Expected: PASS.

- [ ] **Step 5: Commit, when the user says to**

```bash
git add main.go main_test.go testdata/plain.golden
git commit -m "feat: show bare repositories in the help text"
```

### Task 7: documentation and whole-branch verification

**Files:**

- Modify: `README.md`, `CLAUDE.md`, `scripts/compare.sh` (header comment)
- Modify: `docs/plans/2026-11-01-bare-repositories-design.md` (Status line)

- [ ] **Step 1: Update the documentation**

- `README.md`:
  - The example tree near the top gains `🪵 plain.git (2 worktrees)` with two
    🌳 worktrees.
  - "What a line shows" gains a 🪵 row (a bare repository: no working tree,
    its worktrees listed under it) and a `(N worktrees)` row.
  - The bash-script comparison table (the row about linked worktrees) gains a
    row: the script walks into a bare repository; git-dirty lists it with 🪵.
  - "Known limitations": remove "Bare repositories under the scan root are
    not detected; ..."; add `core.worktree` is not read (a git directory that
    sets it is listed as bare), `safe.bareRepository` is not read (git-dirty
    runs nothing from a repository), and 🪵 needs a Unicode 13 emoji font.
- `CLAUDE.md`: the `internal/git` bullet names `IsGitDir` and bare detection
  in `repository.go`; the `internal/scan` bullet says a folder that is itself
  a git directory is a repository, listed as bare with its worktrees.
- `scripts/compare.sh` header: 🪵 lines have no ✅ or ❌ and are skipped; a
  bare repository's 🌳 worktrees are compared like any worktree.
- The spec's Status line: `APPROVED 2026-11-01, implemented on
  feat/bare-repositories`.

- [ ] **Step 2: Run the checks**

Run: `mise run all`
Expected: golangci-lint clean, race tests PASS, govulncheck no findings,
build OK.
Run: `mise run lint:all`
Expected: every hook passes.

- [ ] **Step 3: Rerun the session probe with the new binary**

Run: `go build -o <scratch>/git-dirty . && <scratch>/git-dirty -v <scratch>/bare-probe/root`
Expected: `🪵 dotgit (1 worktree)`, `🪵 lonely.git (0 worktrees)`,
`🪵 plain.git (2 worktrees)` with `plain-feat` and `plain-main` under it,
`🪵 proj (2 worktrees)`; no `[S?]`; no `objects`, `refs` or `worktrees`
folder lines.

- [ ] **Step 4: Benchmark and compare**

Run: `mise run bench`
Expected: within noise of `main` (the walk adds one `Lstat` of `HEAD` per
folder without `.git`); record the numbers in the PR.
Run: `scripts/compare.sh <scratch>/bare-probe/root` and `scripts/compare.sh ~/Code`
Expected: no mismatch other than the documented ones (worktrees outside the
directory, and the bash script's lines for `proj` and `dotgit`, where it runs
`git status` in a folder with no working tree).

- [ ] **Step 5: Commit, when the user says to**

```bash
git add README.md CLAUDE.md scripts/compare.sh docs/plans/2026-11-01-bare-repositories-design.md docs/plans/2026-11-01-bare-repositories.md
git commit -m "docs: document bare repository support"
```
