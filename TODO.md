# TODO

Open work, roughly in priority order. How the system behaves today is in [SPEC.md](./SPEC.md). Each item names the check that proves it done.

## v0.2

- [x] **GitHub webhook.** `Fixes KEY-N` closes issues; mentions add comments. Check: `internal/server/github_test.go`.
- [x] **Account management.** Change own password; admins reset passwords, change roles, disable/enable and delete users (delete only without history). Check: `internal/db/users_test.go`, `internal/server/users_test.go`.
- [x] **Lighter board updates.** SSE events patch the affected card instead of reloading the workspace (`web/src/live.ts`). Check: `web/src/live.test.ts`.
- [x] **Web tests.** Vitest, run by `npm test` in `web/` and in CI.
- [x] **Disabled users stay out of the way.** Hidden from assignee pickers and cannot be assigned; webhook comments for their email are posted as "GitHub"; their open SSE streams end. Check: `web/src/api.test.ts`, `github_test.go`, `TestDisableClosesSSEStream`.
- [x] **Edit and delete.** Edit/delete comments (author or admin), rename projects, delete projects (admin), revoke unused invites, delete cycles. Check: `internal/server/edit_test.go`.
- [x] **Open drawer stays fresh.** The issue drawer reloads on events for its issue, keeping text being edited. Check: `touchesIssue` in `web/src/live.test.ts`.
- [x] **CLI and MCP parity.** Labels, cycles, users, project details, sub-issue filters, delete, export/import, activity in `issue show`. Assignees by email or `me`, cycles by name. Check: `TestMCPProjectToolsAndFriendlyRefs`.
- [x] **Backups.** `velo backup` (`VACUUM INTO`, safe while serving), README section, Docker `HEALTHCHECK`. Check: `TestBackupTo`.
- [ ] **Release v0.2.0.** Bump `web/package.json` version, tag, check that the release workflow uploads all binaries. Also confirm the Docker `HEALTHCHECK` with `docker build` (not tried yet: no Docker on the dev machine).

## Next

- [ ] **Edit cycles from CLI/MCP.** Only create is exposed. `PATCH /api/cycles/{id}` replaces every field, so a partial update needs either a merge in the client or PATCH semantics on the server. Check: MCP test that changes only `status`.
- [ ] **Interactive UI tests.** Current web tests render to static HTML. Clicking through the board (drag and drop, keyboard shortcuts) needs jsdom or a browser; add only if regressions show up there.
- [ ] **Orphaned uploads.** Deleting a project leaves its images in `uploads/`. Low priority; a cleanup command would do. Check: test that unreferenced files are removed.

## Ideas, not committed

Due dates, issue links (blocks, relates to), watchers and in-app notifications, an outgoing webhook.
