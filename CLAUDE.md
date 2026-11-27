@AGENTS.md

# Claude Code workflow for Velo

AGENTS.md holds the rules for everyone. This file only adds how Claude Code works in this repo. SPEC.md is the source of truth for behavior; its Known gaps and Next sections say what to work on.

## Sizing the task

- **Small** (docs, typos, a change under ~20 lines that touches no data or API): make it, run the checks, open the PR. No subagents needed.
- **Everything else**: follow the standard flow.

## Standard flow

1. **Plan.** State assumptions, scope, and a plan as "step → verify: check". Stop and wait for a human when the task would break an Invariant in SPEC.md, add a dependency, change the schema, or change the HTTP API, MCP tools or CLI in a backward-incompatible way.
2. **Test first.** Bug: a test that reproduces it. Feature: tests for the acceptance criteria (the issue, or the "Done when" line of a Known gap). Run them and see them fail.
3. **Implement** the minimum that makes the tests pass.
4. **Run all checks** (below).
5. **Review.** Run the `code-reviewer` subagent. Fix every Critical and High finding and run it again until it says APPROVED. Medium: fix it, or give the reason for leaving it in the PR.
6. **QA.** Run the `qa-tester` subagent. Every failing test it writes is a bug: fix it through steps 2–5.
7. **Docs.** Update SPEC.md and README.md when behavior changes; delete the Known gaps entry you fixed.
8. **PR.** Fill in `.github/pull_request_template.md`. One topic per PR; never push to `main`.

Bug fixes: reproducing test → root cause → fix → rerun the whole suite. Do not patch symptoms.

## Checks

```bash
make check   # build (UI and binary), vet, gofmt, Go and web tests
make race    # go test -race; needs a C compiler; CI runs it too
```

The Makefile is the one place that says what the checks are. CI (`.github/workflows/ci.yml`) runs both, then also checks that the build leaves no tracked file changed, the frontmatter of `.claude/agents/*.md` and `.claude/skills/*/SKILL.md`, and on pull requests the commit message format and the `## Dependencies` section (see CONTRIBUTING.md).

## Evidence

Never say tests pass without running them. In reports and PRs, give the command you ran and its real result. If a check could not run, say so and why.

## Ask first

Get a human's approval before you delete data, touch a real database, rewrite git history, force push, or create a release or tag.
