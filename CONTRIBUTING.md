# Contributing to git-dirty

git-dirty lists the git repositories under a directory as a tree, with each
repository's branch, dirty state, and how far it is ahead of or behind its
upstream. The [README](README.md) describes what it shows and how to use it.

## Development setup

[mise](https://mise.jdx.dev/) installs Go (the `toolchain` line in `go.mod`)
and the development tools pinned in `mise.toml`. The tests also need `git` on
`PATH`.

```sh
mise install
mise run build          # ./git-dirty
mise run hooks:install  # the prek pre-commit hook
```

## Code style

- `.editorconfig` applies to every file; `editorconfig-checker` enforces it.
- golangci-lint (`.golangci.yml`) checks the Go code. Fix its findings rather
  than suppressing them: `//nolint:<linter> // <reason>` is only for code that
  is correct by design.
- The pre-commit hooks (`.pre-commit-config.yaml`) also run markdownlint,
  yamllint, actionlint, zizmor and shellcheck.

```sh
mise run lint:fix   # golangci-lint, applying its fixes
mise run lint:all   # every pre-commit hook on every file
```

## Testing

```sh
mise run test                                  # all tests
mise run test-race                             # with the race detector
go test ./internal/gitstat/ -run TestModified  # one test
go test ./internal/render/ -update             # rewrite golden files; review the diff
```

The tests build real repositories with `git` and use git's own answers as the
expected values. Add a fixture instead of a hand-written expected value.

## Adding a reader to `internal/git`

- `internal/git` imports the standard library only; depguard fails on any
  other import.
- Everything it reads may come from a hostile clone. Keep the size and depth
  limits, and validate ref names with `ValidRefName` before they become paths.
- A decoder returns an error and never panics. A new decoder gets a fuzz target
  in `fuzz_test.go`.

## Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org/):
`feat(scan): …`, `fix(git): …`, `docs: …`. The type decides the release:
[svu](https://github.com/caarlos0/svu) computes each version from the commit
types since the last tag, so a `feat:` or a `!` releases a new minor version.
The README's [Releasing](README.md#releasing) section lists which type releases
what.

## Pull requests

Open pull requests against `main`. CI runs these checks on every pull request:

- Build, vet and test: golangci-lint, the race-enabled tests and the build,
  on Linux
- Test on macOS and Windows
- Vulnerability scan: govulncheck
- CodeQL

`mise run all` runs the Linux checks locally.

## License

git-dirty is MIT licensed ([LICENSE](LICENSE)). Contributions are made under
the same license.
