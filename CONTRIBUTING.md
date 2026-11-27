# Contributing to Velo

Thanks for helping. Velo stays small on purpose, so the bar for a change is "does it solve a real problem with little code", not "is it a nice feature".

## Before you start

- Read [AGENTS.md](./AGENTS.md) (the rules, for humans and coding agents alike) and [SPEC.md](./SPEC.md) (how the system behaves).
- Look at [GitHub Issues](https://github.com/parabol123654/velo/issues) for open work; `good first issue` is a good place to start. Issues titled "Idea:" are not committed yet, so discuss them there before writing code; to propose one, use the Idea issue template.
- Security problems go through [SECURITY.md](./SECURITY.md), never a public issue. For anything bigger than a bug fix, open an issue first and describe the problem and your approach, so nobody spends a weekend on a PR that cannot be merged.

## Setup

You need Go 1.26+, Node.js 20+, Git and GNU Make. The race check (below) also needs a C compiler, because the race detector uses cgo; the Velo binary itself never does. On Debian or Ubuntu: `sudo apt install make build-essential`.

```bash
git clone https://github.com/parabol123654/velo.git
cd velo
make build                     # builds web/dist, then the velo binary
./velo serve --data ./data     # http://localhost:8080
```

For UI work, run `./velo serve` and `cd web && npm run dev` side by side. The dev server on :5173 proxies `/api` to :8080.

## Checks

Run these before opening a pull request. CI runs the same targets from the Makefile, and also checks that the build changes no tracked file, the frontmatter in `.claude/agents/` and `.claude/skills/`, commit messages, and the `## Dependencies` section of the PR (see below).

```bash
make check   # build the UI and binary, go vet, gofmt, Go tests, web tests (Vitest)
make race    # go test -race; needs a C compiler; CI runs it in the race job
```

Every change comes with a test that would fail without it. Bug fixes start with a test that reproduces the bug. Web tests run in Vitest: pure logic in `*.test.ts`, and the whole app against a fake API in `web/src/App.ui.test.tsx` (jsdom and Testing Library). Extend the latter when you change the board, keyboard shortcuts, the drawer or the filter box.

## Rules that decide most reviews

- **No new required services and no CGO.** Velo is one binary and one data directory.
- **Plain SQL in `internal/db/`.** No ORM, no query builder.
- **All writes go through the HTTP API.** The CLI and MCP server call the API instead of opening the database, so every change reaches open browsers over SSE. A new mutation needs a `hub.Broadcast` call and, if the board shows it, handling in `web/src/live.ts`.
- **Schema changes are additive.** An older database must open in the new version; add an idempotent step to `migrate()` and a test for it.
- **New dependencies need a reason.** Say under `## Dependencies` in the PR why the standard library or existing code is not enough. CI fails while that section is empty and `go.mod` or `web/package.json` changed.
- **Keep the diff on topic.** No drive-by reformatting or renames in a feature PR.

## Pull requests

- One topic per PR. Small PRs get reviewed faster.
- If the change alters behavior described in SPEC.md, update SPEC.md in the same PR. Link the issue it closes (`Fixes #12`).
- Commit messages follow `type: summary` with `feat`, `fix`, `docs`, `test`, `chore`, `style` or `ci` and an optional scope (`fix(web): ...`), for example `fix: keep the current session after a password change`. CI checks every commit of the PR except merge commits.
- Describe what changed, why, and how you checked it.
- Fill in the [pull request template](./.github/pull_request_template.md). It records the verdict of the code review (APPROVED, with any Medium finding left open and why) and of QA (QA PASS). Contributors using Claude Code get both from the `code-reviewer` and `qa-tester` subagents in `.claude/agents/`; see [CLAUDE.md](./CLAUDE.md). Small changes as CLAUDE.md defines them, and contributors without Claude Code, write "n/a: maintainer review" in those sections; a maintainer then reviews the PR before merging.
- `main` is protected: changes land only through pull requests with green CI, never by pushing to it.

## Releasing

Maintainers cut releases; coding agents never create tags (see CLAUDE.md).

- **When:** once `main` has changes users would want, and right away for a security fix. Don't let `main` drift far from the latest tag: the README describes `main`, while users run the latest release.
- **Version:** `vMAJOR.MINOR.PATCH`. While Velo is 0.x, bump MINOR for features or anything a user must know about, PATCH for fixes only. A tag with a `-` (`v0.4.0-rc1`) is a pre-release and does not move the Docker `latest` tag. The version exists only in the tag; `web/package.json` is private and its version is not used.

Steps:

1. Pick a commit on `main` whose CI is green. The release workflow builds but does not run tests.
2. Check that README.md and SPEC.md describe that commit, including text that names the new version.
3. Tag and push: `git tag -a v0.4.0 -m v0.4.0 && git push origin v0.4.0`.
4. `.github/workflows/release.yml` then builds the binaries (Linux, macOS, Windows), attaches them to a GitHub release, and publishes the `linux/amd64` and `linux/arm64` image to `ghcr.io/parabol123654/velo` with the tag (and `latest` for a full release).
5. Check the result: the release has five binaries, write its notes (what changed, upgrade steps if any), and `docker run --rm --entrypoint velo ghcr.io/parabol123654/velo:v0.4.0 version` prints the tag.
6. In a follow-up PR, tidy Next and Known gaps in SPEC.md.

## License

By contributing you agree that your work is released under the [MIT License](./LICENSE).
