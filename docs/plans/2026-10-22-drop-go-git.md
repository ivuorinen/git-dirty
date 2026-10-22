# Plan: drop go-git, read git storage in-tree

Date: 2026-10-22
Status: DRAFT — awaiting approval to implement

## Goal

Replace every go-git and go-billy import with a stdlib-only package,
`internal/git`, that reads what `gitstat` needs: repository format, config,
refs, the index (including split index), and objects from loose files and
packs. Then add SHA-1 and SHA-256 repository support. Behavior seen by users
stays the same, except that split-index and SHA-256 repositories become
readable instead of being error lines.

## Decisions (user, 2026-10-22)

| Question | Answer |
| --- | --- |
| Commit-graph reader | **No.** Ahead/behind walks objects only. |
| New formats | **Add split index and SHA-256.** |

## Scope & constraints

- Survey of the 143 repositories in `~/Code` (2026-10-22): index v2 in 141,
  no index in 2. Packs with `.idx` v2 in 137, loose objects in 82,
  `packed-refs` in 138, a commit-graph file in 2. None use: a gitdir file,
  multi-pack-index, alternates, shallow clones, reftable, SHA-256, split
  index or `[include]`. The largest packs are 373 MB and 493 MB.
- `internal/git` imports the standard library only. That covers
  `compress/zlib`, `crypto/sha1`, `crypto/sha256`, `encoding/binary`,
  `path`, `bufio` and `os`.
- Read-only, as today. Nothing executes `git`.
- Must not change: CLI flags, output, exit codes, and every existing
  assertion in the gitstat tests. Those tests compare against the git CLI;
  they are the oracle that proves the swap.
- go-git stays linked until the swap task (Task 12), so every task until
  then lands on a green tree.

### What go-git does for us today, and its replacement

| Today (go-git) | Replacement in `internal/git` |
| --- | --- |
| `storage/filesystem`, `dotgit`, `osfs`, `cache` | `Repository`: git dir, common dir, open packs, base cache |
| `plumbing/format/config`, `config.Config`, `RefSpec` | `config.go`, `refspec.go` |
| refs storage, `storer.ResolveReference` | `refs.go` |
| `plumbing/format/index` | `index.go`, `splitindex.go` (EWAH) |
| objects, packfiles, deltas | `odb.go`, `pack.go`, `delta.go` |
| `object.Commit`, `Tree`, `Tag`, `TreeWalker` | `objects.go` |
| `gitignore` patterns and matcher | `ignore.go` |
| `commitgraph`, `object/commitgraph` | dropped; the walk uses commit time |
| `plumbing.Hash`, hashers | `hash.go`: 20- or 32-byte ids |

## Tasks

Every task lands green: `go test -race ./...`, `gofmt -l .` empty,
`go vet` and `staticcheck` clean, editorconfig-checker clean. Reader tests
build fixtures with the `git` binary and compare against git's own output,
as the gitstat tests already do.

1. **[readers] Hash and repository format** — `internal/git/hash.go`,
    `format.go`.
    - `ObjectID`: a comparable, map-keyable value holding 20 or 32 bytes,
      plus hex parsing, formatting and `IsZero`.
    - A `Format` per repository: hash size, and the hasher (`crypto/sha1` or
      `crypto/sha256`). It comes from `extensions.objectFormat`; SHA-1 when
      unset.
    - Format check: with `core.repositoryFormatVersion = 1`, an extension
      other than `objectformat`, `worktreeconfig`, `partialclone`, `noop`
      and `preciousobjects` is an error naming it. That includes
      `refstorage=reftable`. This matches git, which refuses repositories
      with extensions it doesn't know.
    - Verify:
      - Blob ids for sample files equal `git hash-object` in a SHA-1 repo
        and in a `git init --object-format=sha256` repo.
      - A config with `[extensions] refstorage = reftable` and version 1
        gives an error naming `refstorage`.

2. **[readers] Config parser** — `internal/git/config.go`.
    - Sections and keys case-insensitive. `[section "subsection"]` with a
      case-sensitive subsection and `\"`/`\\` escapes; the legacy
      `[section.subsection]` form.
    - A bare key means true. Quoted values, escapes (`\n \t \b \\ \"`),
      inline `;`/`#` comments outside quotes, backslash-newline
      continuation, trimmed whitespace.
    - Multi-valued keys (`GetAll`) for `remote.*.fetch`.
    - Booleans as git parses them: true/yes/on/1 and false/no/off/0.
    - `[include]` is not followed, as today.
    - Verify: on one fixture file containing every construct above, each
      key's value equals `git config --file <f> --get` / `--get-all`. A
      malformed file (unclosed section, unterminated quote) gives an error
      with the line number, where `git config --file <f> --list` also
      fails.

3. **[readers] Refs** — `internal/git/refs.go`.
    - HEAD and other pseudo-refs come from the worktree's git dir; `refs/`
      from the common dir.
    - Loose refs: `ref: <target>` or a hex id. `packed-refs`: honor the
      `peeled` header and `^<id>` lines. A loose ref overrides a packed
      one.
    - Symbolic refs resolve to a depth of 5, as git's `SYMREF_MAXDEPTH`.
    - `Tags()` iterates loose and packed tags, returning peeled ids when
      `packed-refs` has them.
    - **Ref name validation** before any filesystem access. Ref names come
      from repository config (`branch.*.merge`), which a hostile clone
      controls. Reject `..`, a leading `/`, `//`, a trailing `.lock`,
      control characters, `\`, and anything outside `refs/` except
      `HEAD`. A rejected name is an error, never a read.
    - Verify:
      - Against `git show-ref`, `git rev-parse <ref>` and
        `git symbolic-ref`: loose, packed (`git pack-refs --all`), packed
        plus a loose override, annotated tags peeled through
        `packed-refs`, a symref chain, and a self-referencing symref (an
        error, not a hang).
      - The name table: `refs/heads/../../config` and `refs/heads/a\x00`
        are rejected.

4. **[readers] Refspec mapping** — `internal/git/refspec.go`.
    - `[+]src:dst` with at most one `*` per side. A negative (`^…`)
      refspec excludes a match. `Match` and `Dst` behave like go-git's
      `RefSpec`, which `upstreamRef` uses today.
    - Verify:
      - Table tests: the default fetch refspec, an exact refspec, a
        `*`-in-middle refspec (`refs/heads/team/*/x:refs/remotes/o/*`), a
        negative refspec, and a malformed refspec (two `*`) that matches
        nothing.
      - End to end, `TestUpstream` (unchanged) after the swap.

5. **[readers] Index reader** — `internal/git/index.go`.
    - Versions 2, 3 (extended flags: skip-worktree, intent-to-add) and 4
      (path prefix compression).
    - Fields: stat data (ctime, mtime, dev, ino, uid, gid, size), mode,
      id, stage, flags.
    - `TREE` (cache-tree) is decoded. Other optional extensions
      (uppercase first letter: `REUC`, `UNTR`, `FSMN`, `EOIE`, `IEOT`) are
      skipped by length. A mandatory unknown extension is an error naming
      it, `sdir` (sparse index) included.
    - The index file's mtime is recorded for the racy-git check. The
      trailing checksum is not verified (`index.skipHash` writes zeros).
    - Verify, for an index in each of v2, v3 (`git add -N` plus
      `--skip-worktree`) and v4 (`git update-index --index-version 4`) in
      both a SHA-1 and a SHA-256 repo:
      - every entry's mode, id, stage and name equals
        `git ls-files --stage`
      - flags equal `git ls-files --debug` / `-t`
      - when `git ls-files` reports no changes, the root cache-tree id
        equals `git write-tree`
      - a `git sparse-checkout init --cone --sparse-index` repo gives the
        `sdir` error

6. **[readers] Split index** — `internal/git/splitindex.go`.
    - The `link` extension: shared index id, plus EWAH-compressed delete
      and replace bitmaps.
    - Load `sharedindex.<hex>` from the git dir. The name must be the hex
      id itself, never a path.
    - Apply deletions and replacements; replaced entries take their name
      from the shared entry (git strips it in the split index). Append
      the new entries and sort by name.
    - EWAH decoder: run-length words plus literal words, with every count
      bounds-checked.
    - Verify against `git ls-files --stage`:
      - after `git update-index --split-index`
      - after an add, a modification and a deletion on top of it
      - after `git update-index --no-split-index`

      EWAH unit tests decode bitmaps git wrote. A truncated bitmap and an
      out-of-range bit are errors.

7. **[readers] Object database** — `internal/git/odb.go`, `pack.go`,
    `delta.go`.
    - Loose objects: zlib with a `<type> <size>\0` header; the declared
      size must match the content.
    - Packs:
      - `.idx` v2 lookup by `ReadAt`: fanout table, then a binary search
        of the name table, then 4- or 8-byte offsets (large offsets
        included). The index is never loaded whole; a 493 MB pack has an
        index of several tens of MB.
      - `.pack` entry header, `OFS_DELTA` (git's offset encoding),
        `REF_DELTA`, and delta application (copy and insert opcodes,
        where copy size 0 means 0x10000).
    - Packs stay open for the repository's lifetime; `Close` closes them.
    - A delta-base cache per repository, bounded to 16 MiB, keyed by
      (pack, offset).
    - **Limits** (any breach is an error, never a panic):
      - a declared object size above 256 MiB
      - a delta result that does not match its declared size
      - a copy opcode reaching outside the base
      - a delta chain deeper than 10,000 (git's maximum pack depth is
        4,095), which also stops `REF_DELTA` cycles
      - zlib output beyond the declared size (read through
        `io.LimitReader`, then checked for EOF)
    - Verify:
      - **Self-check:** for every object `git cat-file
        --batch-all-objects --batch-check` lists, the type and size match,
        and hashing `"<type> <size>\0" + content` reproduces the id. Run
        it on:
        - a loose-only repo
        - a `git gc` repo
        - `git repack -adf --depth=50 --window=250` (deep `OFS_DELTA`)
        - `git -c repack.useDeltaBaseOffset=false repack -adf`
          (`REF_DELTA`)
        - a SHA-256 repo
      - A synthetic `.idx` with a large-offset table resolves an 8-byte
        offset.
      - Corrupted-pack table tests trip each limit: wrong size, a
        truncated delta, an out-of-range copy, a self-referencing
        `REF_DELTA`.
      - Dev-only (`go test -run TestODB_RealRepos -realrepos`), not in CI:
        the same self-check over the five largest repos in `~/Code`.

8. **[readers] Object parsing** — `internal/git/objects.go`.
    - Commit: tree, parents (octopus included), committer time. `gpgsig`
      and `mergetag` continuation lines are skipped.
    - Tag: target id and type. Tree entries: mode, name and id, with git's
      mode canonicalization (for example legacy `100664` → `100644`) and
      a recursive walk yielding full paths.
    - Verify, on fixtures with a signed commit (a fixed gpgsig header
      written via `git hash-object -t commit -w`), a mergetag, an octopus
      merge and a tree with every mode:
      - fields equal `git cat-file -p` and `git log --format=%T%x00%P%x00%ct`
      - tree walk paths and modes equal `git ls-tree -r`

9. **[readers] Fuzz targets** — `internal/git/fuzz_test.go`.
    - `FuzzIndex`, `FuzzEWAH`, `FuzzConfig`, `FuzzPackIdx`,
      `FuzzPackEntryAndDelta`, `FuzzCommit`, `FuzzTree` and `FuzzRefs`,
      seeded from the Task 2–8 fixtures. The property is no panic, no
      allocation beyond the limits, and termination.
    - Seed corpora run as ordinary tests in CI.
    - Verify: each target runs `-fuzztime=60s` locally with no failure.
      Every crasher found is fixed and added to `testdata/fuzz/`.

10. **[readers] gitignore matcher** — `internal/git/ignore.go`.
    - Comments, blanks, `\#`/`\!` escapes, trailing spaces trimmed unless
      escaped, `!` negation, a trailing `/` for dir-only.
    - A leading or middle `/` anchors the pattern to its file's directory.
      `**/`, `/**` and `/**/` per gitignore(5).
    - Per-segment globs through `path.Match`, with `[!…]` translated to
      `[^…]`. A malformed glob never matches, as git does.
    - Case-folded when `core.ignorecase=true`, as git does.
    - Verify: `TestIgnore_*` (unchanged) moved onto the new matcher, plus
      `git check-ignore` cases for `**` in each position, escaped `#`/`!`,
      character classes and an ignorecase repo.

11. **[swap] Rewire gitstat** — `internal/gitstat/*.go`.
    - `Repo` wraps `git.Repository`. `head`, `dirty`, `ignore`, `refs`,
      `aheadbehind` and `inspect` call the new readers.
    - `newCommitWalker` drops the commit-graph branch: every commit node
      has an infinite generation, so the existing date ordering with
      `walkSlop` and the late recount does the work.
    - `stageMerged` and the `LoadGlobalPatterns` comment go with go-git.
    - Test helpers that reached into go-git (`r.st`, `storer`) change to
      the new API. **No assertion changes.**
    - Verify:
      - `go test -race ./...` is green with `git diff` on
        `internal/gitstat/*_test.go` showing helper changes only.
      - `TestAheadBehind/*/commit-graph` still passes; results don't
        depend on the commit-graph now.
      - `scripts/compare.sh` reports no mismatches on `~/Code`.

12. **[swap] Remove go-git** — `go.mod`, `go.sum`.
    - `go mod tidy`.
    - Verify:
      - `go list -deps .` shows no `go-git` or `go-billy`. `go list -m
        all` lacks the 14 modules only go-git pulled in (circl,
        filepath-securejoin, gods, gcfg, go-billy, go-git, go-context,
        sha1cd, go-crypto, go-diff, x/crypto, x/net, warnings; x/sys stays
        for charm).
      - `govulncheck` reports nothing in required modules.
      - `TestInspect_ReleasesDescriptors` fails when `Repository.Close`
        is mutated to skip closing packs (packs are now held open, so the
        test finally guards `Close`).

13. **[formats] Split-index and SHA-256 end to end** —
    `internal/gitstat/inspect_test.go`, `dirty_test.go`.
    - The `TestInspect_Errors/split index` case becomes a success case.
      This deliberately reverses the documented limitation; it is not a
      weakened test.
    - Verify:
      - On a split-index repo with a modified, a staged and an untracked
        file, M/S/? equal git's verdicts.
      - A SHA-256 repo runs the whole `TestModified`, `TestStaged`,
        `TestUntracked`, `TestStashed`, `TestAheadBehind` and `TestUpstream`
        tables, parametrized by object format.
      - A `--sparse-index` repo still reports the `sdir` error.

14. **[swap] Performance check** — no code unless it regresses.
    - `hyperfine --warmup 3` comparing the go-git build (saved as
      `git-dirty.go-git` before Task 11) with the new build on `~/Code`.
    - Verify: the new mean is no slower than the old one plus its σ. If it
      is slower, profile with `go test -cpuprofile`, fix inside
      `internal/git`, and re-measure.

15. **[docs] Docs and release config** — `README.md`, `CLAUDE.md`,
    `.goreleaser.yaml`.
    - README limitations: split index and SHA-256 are now supported;
      sparse index, reftable, alternates and `[include]` are not; the
      commit-graph is not used.
    - CLAUDE.md: replace the go-git rules with the `internal/git` rules
      (stdlib only, the limits, fuzz targets, ref-name validation).
    - The goreleaser notices comment.
    - Verify:
      - A snapshot release's `THIRD_PARTY_NOTICES.txt` covers every
        linked module and no longer lists MPL-2.0.
      - The flag/env/key cross-check script from the first plan reports
        no gaps.

## Adversarial hardening

- **complexity** — What was cut:
  - The commit-graph reader (user decision; 2 of 143 repos have one).
  - multi-pack-index, alternates, bitmap and `.rev` readers: none are used
    in `~/Code`, and `.idx` files stay alongside a multi-pack-index.
    Alternates get a clear error, not support.
  - Index checksum verification: it doesn't protect against a hostile
    writer, who can recompute it, and `index.skipHash` writes zeros.
  - A per-object hash check at runtime: it lives in tests only.

  What was kept and why: the delta-base cache. Without it, the staged
  tree-walk fallback on a deeply deltified tree history decompresses each
  base once per dependent object; it is bounded at 16 MiB.
- **review** — Edge cases fixed as fixtures: index v2/v3/v4, split index
  with each mutation kind, EWAH run and literal words, `OFS`/`REF` deltas,
  copy size 0, large `.idx` offsets, loose-over-packed refs, peeled packed
  tags, symref chains and loops, octopus merges, `gpgsig`/`mergetag`
  headers, legacy tree modes, `**` in three positions, escaped ignore
  patterns, and config quoting/continuation/legacy subsections.
- **security** — The trust boundary is unchanged (cloned repositories), but
  the parsing that crosses it is now ours. Added in response:
  - the Task 7 limits on sizes, chain depth, copy bounds and zlib output
  - ref-name validation, because `branch.*.merge` is attacker-written and
    flows into a file path (Task 3)
  - a hex-only shared index name (Task 6)
  - symref depth 5
  - fuzz targets for every decoder (Task 9)

  Accepted: SHA-1 collision detection (`sha1cd`) is dropped. A crafted
  collision could at most make a file look unchanged in a read-only
  report.
- **errors / leaks** — Every decoder returns errors and never panics on
  input; the fuzz property checks it. Open packs are owned by the
  `Repository` and closed in `Close`; the Task 12 mutation proves the
  descriptor test guards it. zlib readers are closed per object.
- **migrations** — N/A: no stored data or schema.
- **concurrency** — The `Repository`, its open packs and its base cache
  belong to one worker goroutine, as the go-git storage does today. The
  global excludes stay immutable and shared. No locks are added; `-race`
  covers it.
- **contract** — CLI, output and exit codes are unchanged. Behavior
  changes:
  - split-index and SHA-256 repos are read instead of erroring
  - repos with unknown repository-format extensions now error, as git does
  - commit-graph presence no longer changes the walk (results are
    identical)

  These go in the README. CalVer needs no bump decision.
- **arch** — `internal/git` is a leaf: stdlib only, no output, no knowledge
  of `gitstat`. Task 12's `go list -deps` check holds the stdlib-only rule
  for the binary as a whole. CLAUDE.md records the rule.
- **perf** — `.idx` lookups by `ReadAt` binary search rather than loading
  the index (the go-git storage loads it). Packs are opened once per repo,
  not per read as go-git v5 does. The base cache is bounded. Task 14
  measures against the go-git build and requires no regression.
- **tests** — The gitstat suite stays the unchanged oracle (Task 11 forbids
  assertion edits). New readers are each checked against a git command, and
  the object store against content hashing, which needs no hand-written
  expected values. Fuzz targets cover inputs git would never write.
- **license** — Removes the MPL-2.0 module and 13 others; the notices
  shrink (Task 15).
- **config / privacy / a11y / i18n / observability** — N/A: no new
  settings, no personal data, no UI or text changes, and no new signals.
  Reader errors keep flowing to `Result.Err` with a stage prefix.

## Rollback / abort

- Tasks 1–10 add a package nothing calls yet; dropping them is deleting
  `internal/git`.
- Task 11 is one revertible change; go-git is still in `go.mod` until Task
  12, so reverting Task 11 alone restores the current behavior.
- Abort criterion: if Task 11 cannot reach a green suite without changing an
  assertion, or Task 14 shows a regression that profiling cannot remove,
  stop and report. Keeping go-git is then the fallback, and that is a user
  decision.

## Open questions & accepted risks

- **Accepted — no SHA-1 collision detection** (see security).
- **Accepted — still unsupported:** sparse index (`sdir`), reftable,
  alternates, `[include]` and commit-graph. Each gives an error line or is
  ignored, and none appears in `~/Code`.
- **Accepted — the index checksum is not verified.**
- **Accepted — the delta chain limit is 10,000**, above git's maximum
  depth of 4,095, so any pack git writes is readable.
- **Assumption — git 2.43** (local) can create every fixture used:
  `--object-format=sha256`, `--split-index`, `--sparse-index` and
  `--index-version 4` all predate it. Reftable fixtures need git ≥ 2.45,
  which is why Task 1 tests reftable through a config fixture.
- **Assumption — SHA-256 support in git 2.43 is marked experimental** but
  its on-disk format is stable. Interop with SHA-1 remotes is not needed
  for local reads.
- **Correction to the earlier estimate:** split index needs an EWAH decoder,
  so Task 6 is about 100 lines, not 40. Overall: about 1,700–2,000 lines
  of code plus tests, roughly the size of `gitstat` again.
