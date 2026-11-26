# TODO

Open work, roughly in priority order. How the system behaves today is in [SPEC.md](./SPEC.md). Each item names the check that proves it done.

## v0.2

- [x] **GitHub webhook.** `Fixes KEY-N` closes issues; mentions add comments. Check: `internal/server/github_test.go`.
- [x] **Account management.** Change own password; admins reset passwords, change roles, disable/enable and delete users (delete only without history). Check: `internal/db/users_test.go`, `internal/server/users_test.go`.
- [x] **Lighter board updates.** SSE events patch the affected card instead of reloading the workspace (`web/src/live.ts`). Check: `web/src/live.test.ts`.
- [x] **Web tests.** Vitest, run by `npm test` in `web/` and in CI. Covers the live-update reducer and Markdown rendering and checklists.
- [ ] **Release v0.2.0.** Bump `web/package.json` version, tag, check that the release workflow uploads all binaries.

## Next

Gaps found while comparing the API with the CLI, MCP and UI.

- [ ] **Hide disabled users from pickers.** The assignee and filter selects still list disabled users. Check: a unit test on the filter helper.
- [ ] **Disabled users and Git.** The webhook still posts as a disabled user whose email matches the commit. Decide: post as "GitHub" instead? Check: case in `github_test.go`.
- [ ] **Close live SSE streams on disable.** A disabled user's open `/api/events` stream keeps receiving events until it reconnects. Check: server test that the stream ends.
- [ ] **CLI and MCP parity.** No commands or tools yet for labels, cycles, states, sub-issues, deleting issues, export/import or activity. Check: extend `internal/mcp/mcp_test.go`.
- [ ] **Edit and delete.** No way to edit or delete comments, rename or delete projects, revoke invites, or delete cycles. Check: API tests per endpoint.
- [ ] **Open drawer stays stale.** The issue drawer does not refresh when someone else changes the open issue. Check: `applyEvent`-style test for the drawer.
- [ ] **Interactive UI tests.** Current web tests render to static HTML. Clicking through the board (drag and drop, keyboard shortcuts) needs jsdom or a browser; add only if regressions show up there.
- [ ] **Backups.** Document backing up a live WAL database (`sqlite3 velo.db ".backup ..."` or `VACUUM INTO`), and add a Docker `HEALTHCHECK` on `/api/health`.

## Ideas, not committed

Due dates, issue links (blocks, relates to), watchers and in-app notifications, an outgoing webhook.
