# Contributing to Velo

Thanks for helping. Velo stays small on purpose, so the bar for a change is "does it solve a real problem with little code", not "is it a nice feature".

## Before you start

- Read [AGENTS.md](./AGENTS.md) (the rules, for humans and coding agents alike) and [SPEC.md](./SPEC.md) (how the system behaves).
- Look at [GitHub Issues](https://github.com/parabol123654/velo/issues) for open work; `good first issue` is a good place to start. Issues titled "Idea:" are not committed yet, so discuss them there before writing code. For anything bigger than a bug fix, open an issue first and describe the problem and your approach, so nobody spends a weekend on a PR that cannot be merged.

## Setup

You need Go 1.26+ and Node.js 20+.

```bash
git clone https://github.com/parabol123654/velo.git
cd velo
make build                     # builds web/dist, then the velo binary
./velo serve --data ./data     # http://localhost:8080
```

For UI work, run `./velo serve` and `cd web && npm run dev` side by side. The dev server on :5173 proxies `/api` to :8080.

## Checks

Run these before opening a pull request. CI runs the same ones.

```bash
make test                      # Go tests
make test-web                  # web tests (Vitest)
go vet ./...
gofmt -l internal cmd          # must print nothing
cd web && npm run build        # type-checks and builds the UI
```

Every change comes with a test that would fail without it. Bug fixes start with a test that reproduces the bug.

## Rules that decide most reviews

- **No new required services and no CGO.** Velo is one binary and one data directory.
- **Plain SQL in `internal/db/`.** No ORM, no query builder.
- **All writes go through the HTTP API.** The CLI and MCP server call the API instead of opening the database, so every change reaches open browsers over SSE. A new mutation needs a `hub.Broadcast` call and, if the board shows it, handling in `web/src/live.ts`.
- **Schema changes are additive.** An older database must open in the new version; add an idempotent step to `migrate()` and a test for it.
- **New dependencies need a reason.** Say in the PR why the standard library or existing code is not enough.
- **Keep the diff on topic.** No drive-by reformatting or renames in a feature PR.

## Pull requests

- One topic per PR. Small PRs get reviewed faster.
- If the change alters behavior described in SPEC.md, update SPEC.md in the same PR. Link the issue it closes (`Fixes #12`).
- Commit messages follow `type: summary` with `feat`, `fix`, `docs`, `test`, `chore` or `style`, for example `fix: keep the current session after a password change`.
- Describe what changed, why, and how you checked it.
- Fill in the [pull request template](./.github/pull_request_template.md). Every PR records the verdict of the code review (APPROVED, with any Medium finding left open and why) and of QA (QA PASS). Contributors using Claude Code get both from the `code-reviewer` and `qa-tester` subagents in `.claude/agents/`; see [CLAUDE.md](./CLAUDE.md).
- `main` is protected: changes land only through pull requests with green CI, never by pushing to it.

## License

By contributing you agree that your work is released under the [MIT License](./LICENSE).
