# Velo spec

What Velo is and the rules it keeps. This describes the current system, not a plan; open work lives in [GitHub Issues](https://github.com/parabol123654/velo/issues) (the order to take it in is under [Next](#next)), and defects in the current system are listed under [Known gaps](#known-gaps). Contributor rules are in [AGENTS.md](./AGENTS.md).

## Goal

A self-hosted issue tracker for individuals and small dev teams (1–25 people) and their coding agents. It covers the part of Jira small teams actually use, and nothing else.

Non-goals: enterprise permission matrices, custom fields, workflow engines, mail servers, plugins, multi-tenant hosting.

## Invariants

These hold for every change. Breaking one needs an explicit decision, recorded here.

1. **One binary, one data directory.** The web UI is embedded with `embed.FS`. Data is `<data>/velo.db` (SQLite, WAL, FTS5) plus `<data>/uploads/`. No CGO (`modernc.org/sqlite`), no required external service.
2. **Plain SQL.** Queries live in `internal/db/` on `database/sql`. No ORM.
3. **One write path.** The web UI, the CLI and the MCP server all change data through the HTTP API, so validation is shared and every mutation reaches open browsers over SSE. The CLI and MCP never open the SQLite file. Two exceptions, both server-side or offline: `velo backup` opens the file to run `VACUUM INTO` (and, like any open, runs `migrate()`), and the GitHub webhook writes through `internal/db` inside the server process and broadcasts itself.
4. **Schema changes are additive and automatic.** `migrate()` runs the `CREATE ... IF NOT EXISTS` schema, then adds columns that older databases lack (`addColumn`). A v0.1 database must open in any later version.
5. **Every change ships with a check:** `go test ./...`, `npm test` and `npm run build` in `web/`.

## Domain

| Concept | Rules |
| :-- | :-- |
| Project | Unique `key` of 2–10 uppercase letters or digits, starting with a letter (`DEV`). Issue numbers come from `issue_seq`, incremented in the same transaction as the insert, so keys never collide. |
| Workflow state | Per project, any name, but each belongs to one category: `backlog`, `unstarted`, `started`, `completed`, `canceled`. New projects get Backlog, Todo, In Progress, In Review, Done, Canceled. |
| Issue | `KEY-N` identifier, optional parent in the same project (sub-issues; deleting a parent detaches its children), Markdown description, priority 0–4 (none, low, medium, high, urgent), optional assignee and cycle, labels by ID or name. A `PATCH` sends only the fields to change; there is no version check, so the last write wins. |
| Issue link | Between two issues of the same project: `blocks`, `relates` (symmetric) or `duplicates`. The API also takes `blocked_by` and `duplicated_by` and stores the reversed form. An issue's `blocked_by_count` counts blockers not yet completed or canceled; finishing, reopening or deleting a blocker pushes `issue.updated` for the issues it blocks. Unlinking removes every link between the pair. |
| Picking up work | `next` returns the issue to start: state category `unstarted`, unassigned, not blocked; highest priority first with "no priority" last, then board order. Optional project and label filters. `claim` assigns it to the caller and moves it to the project's first `started` state; it fails with 409 if someone else holds it, it is finished, or it is blocked. The check and the write run in one transaction with a conditional `UPDATE`, so of a claim and a concurrent claim or manual assignment exactly one takes effect; updates always read the current row inside their transaction. |
| Checklist | `- [ ]` / `- [x]` lines in a description, outside code blocks. Clients can set one item by 1-based index or by text (exact match first, else a unique substring); setting is idempotent and logs a short `checklist` activity. |
| Ordering | `position REAL`. A drop between two cards takes the midpoint, so a move updates one row. |
| Cycle | Optional sprint with dates and status `upcoming`, `active`, `completed`. |
| Comment, activity, attachment | Comments are Markdown; the author or an admin may edit or delete one. Activities record creation; changes to title, state, priority, assignee and cycle; links (`link`, on the issue named in the request only); and checklist ticks (`checklist`). Description edits, labels and parent changes leave no activity. Uploads (max 20 MB) go to `uploads/` and are served only to signed-in users; PNG, JPEG, GIF and WebP display inline, anything else downloads. |
| Search | FTS5 (default `unicode61` tokenizer) over identifier, title and description, kept in sync by triggers. Each word is a prefix match, all words must match. Comments are not indexed. |
| Export/import | Per project, Markdown (read-only) or JSON (round-trips through import, except issue links, which are not exported yet). |

## Accounts and access

- **First run:** the first visitor creates the admin. After that, accounts come from single-use invite links (7 days) or are created by an admin. No email is sent.
- **Roles:** `admin` and `member`. Members can do everything with projects and issues. Only admins manage users and invites.
- **Credentials:** browser sessions are `HttpOnly` cookies valid 30 days (`vs_` tokens, also accepted as Bearer). API tokens (`velo_` prefix) are for the CLI and MCP. Only SHA-256 hashes are stored; passwords use bcrypt, minimum 6 characters.
- **Password change:** a user changes their own password with the current one. Their other sessions are signed out; the current one stays.
- **Admin actions:** reset a user's password (signs out all their sessions), change role, disable or enable, delete.
- **Disable:** login, sessions and API tokens stop working at once, and sessions are deleted. Tokens come back if the user is enabled again. History stays intact.
- **Delete:** only for users with no history (created issues, comments, activity, uploads). Otherwise the API answers 409 and the admin disables instead.
- **Guards:** an admin cannot change or delete their own account through these endpoints, and the last enabled admin cannot be demoted, disabled or deleted.
- **Login throttling:** after 5 failed logins within 15 minutes from one client IP or for one email, further attempts (even correct ones) get 429 with `Retry-After` until 15 minutes after the first failure. A successful login clears the email's counter. Counters live in memory. The IP is the TCP peer, so behind a reverse proxy all clients share one counter.

### API token scopes

A token acts as its user, narrowed by its scope (set at creation, shown in Settings):

- **Access:** `write` (default) or `read`. A read token may only use `GET`.
- **Projects:** empty means every project. Otherwise the token only sees and changes those projects: lists, search and the SSE stream are filtered; requests for objects elsewhere get 403; creating or importing projects is refused. Deleting a listed project does not widen the token.
- **Never with a token:** managing tokens, changing the password, and every admin endpoint, even for an admin's token.
- Every authenticated route declares its token rule in `routes()` (`internal/server/scope.go`), so a new route cannot skip the check.
- Attachments not yet linked to an issue are served to any signed-in caller; their random URL is the capability.

### Attribution

Comments and activities made through an API token record the token's name (`via`), shown as "via Claude Code" in the UI and CLI and included in the JSON and SSE payloads. Browser sessions record nothing, so people and agents can be told apart.

## Live updates

`GET /api/events` is a Server-Sent Events stream. Each message is `{"type": ..., "payload": ...}`:

| Event | Payload | Web UI handling |
| :-- | :-- | :-- |
| `issue.created`, `issue.updated` | full issue | Upsert that card; refetch affected parent cards; debounced refetch of project and cycle counters |
| `issue.deleted` | `{id, identifier}` | Remove the card, detach its sub-issues |
| `comment.created`, `comment.deleted` | comment, or `{id, issue_id}` | Adjust the card's comment count |
| `comment.updated` | comment | Nothing on the board; an open drawer refreshes |
| `project.*`, `state.updated`, `cycle.*`, `user.updated`, `user.deleted` | object | Debounced full workspace reload |

Linking, unlinking, claiming and checklist ticks send `issue.updated` for every issue whose data changed. Label changes and new users (created by an admin or through an invite) are not broadcast yet; see [Known gaps](#known-gaps).

Each event belongs to a project (user events to none); a project-restricted token's stream only gets its projects' events. The board logic is the pure function `applyEvent` in `web/src/live.ts`. A slow client may miss events (the hub drops them when its buffer is full); the UI reloads once whenever the stream reconnects.

## Integrations

- **CLI:** `velo serve | login | project | issue | label | cycle | user | search | mcp | backup | version`. `velo issue` covers `list | show | create | update | delete | comment | next | claim | check | link | unlink`. `VELO_URL` and `VELO_TOKEN` override the saved config (`~/.config/velo/config.json`).
- **MCP (stdio):** hand-written JSON-RPC in `internal/mcp` (no SDK), protocol version `2024-11-05`, results as text. It calls the HTTP API through `internal/client`, so it needs a running server and a token. Tools `list_projects`, `get_project`, `list_users`, `list_issues`, `get_issue`, `create_issue`, `update_issue`, `delete_issue`, `next_issue`, `claim_issue`, `check_item`, `link_issues`, `unlink_issues`, `add_comment`, `search_issues`, `create_label`, `create_cycle`, `export_project`.
- **MCP resources:** `velo://project/<KEY>` (listed) and `velo://issue/<KEY-N>` (template), as Markdown: an issue includes its links, sub-issues and comments.
- **MCP prompts:** `work_on_issue` (claim → work → `check_item` → comment → review), `split_issue` (propose, then create sub-issues and `blocks` links), `summarize_cycle`. Each embeds the current issue or cycle so the agent starts with context.
- **Agent runner (`velo agent`):** a client-side loop, never run by the server. It wakes on SSE events (and every `--poll`), takes `next` for its label/project, `claim`s it, runs `--cmd` with the `work_on_issue` prompt, and comments the result (exit code, duration, last ~3 KB of output). The command is split into arguments and executed without a shell; the prompt is passed as one argument (`{prompt}`) or on stdin. One run at a time; a failed, timed-out or interrupted run leaves the issue assigned to the agent, so it is never retried automatically. A failed claim waits for the next event instead of retrying.
- **Friendly references:** wherever the API takes an assignee it also accepts an email or `me`; cycles accept a name; parent filters accept an identifier. Disabled users cannot be assigned.
- **GitHub webhook:** `POST /api/webhooks/github`, enabled by `--github-secret` or `VELO_GITHUB_WEBHOOK_SECRET`. Commits and PRs that mention `KEY-N` get a comment. `Fixes`, `Closes` or `Resolves KEY-N` on the default branch (or a PR merged into it) moves the issue to the first completed state. Comments are posted as the user with the matching email, otherwise as a built-in "GitHub" user that cannot log in.

## Known gaps

Places where the code falls short of this spec or of the rules in [CONTRIBUTING.md](./CONTRIBUTING.md). Each is small; fix it test-first (the "Done when" line is the test), then delete the entry in the same PR. Last reviewed at commit `ceaf004` on 2026-11-26.

2. **Parent loops are accepted.** `UpdateIssue` rejects an issue as its own parent but not a loop (`A` under `B`, then `B` under `A`). Fix: walk up from the new parent and refuse if the issue appears. Done when: that patch returns 400 and the data is unchanged.
3. **Blocking loops are accepted.** `LinkIssues` refuses `B blocks A` when `A blocks B` exists, but not longer loops (`A blocks B`, `B blocks C`, `C blocks A`). Every issue in the loop stays blocked forever and `next_issue` skips them without saying why. Fix: before inserting a `blocks` link, refuse when the target already blocks the source through any chain. Done when: closing a three-issue loop returns 409 naming the chain, and the link is not stored.
4. **Unknown labels are dropped silently.** `CreateIssue` and `UpdateIssue` skip label IDs or names that do not exist in the project, so an agent that misspells a label gets 200 and no label. Fix: return 400 naming the unknown labels. Done when: API and MCP tests show the error and no partial write.
5. **Some mutations do not broadcast.** `handleCreateLabel` and `handleDeleteLabel` never call `hub.Broadcast`; neither do `handleCreateUser` and `handleAcceptInvite`. Open boards miss new labels and new assignees until reload. Fix: broadcast `label.created`, `label.deleted` (project-scoped) and `user.created`, and let `applyEvent` treat them like `project.*`. Done when: server tests assert the events and `live.test.ts` covers them.
6. **Activity history is incomplete.** Description, labels and parent changes leave no activity. A link is logged only on the issue named in the request, and an unlink records the other issue but not the link kind. Fix: log `description` (field only, no text), `labels` (old and new names) and `parent` (old and new identifier); log links and unlinks on both issues with the kind. Done when: a test changes each field and finds one activity per change and none for a no-op, and both linked issues show the link and the unlink.
7. **Export drops issue links.** `ExportProjectJSON` and `ImportProjectJSON` (`internal/db/export.go`) ignore `issue_links`, so a JSON round trip loses blockers. Fix: export links by identifier and recreate them on import after all issues exist. Done when: the round-trip test in `db_test.go` includes a `blocks` and a `relates` link and they come back.
8. **Vietnamese search does not fold accents.** The default `unicode61` tokenizer (`remove_diacritics 1`) leaves letters with two marks (`ậ`, `ố`) and `đ` unfolded, so `dang nhap` does not find `đăng nhập`. The fix needs a decision recorded here first, because it rebuilds the FTS table (derived data, so invariant 4 still holds): recreate `issues_fts` with `tokenize='unicode61 remove_diacritics 2'`, and fold `đ`/`Đ` to `d` in the indexed text and the query. Done when: a test finds `đăng nhập` with `dang nhap`, and a database created by the previous release opens and is reindexed.
9. **Uploads outlive their issue.** Deleting an issue or project sets `attachments.issue_id` to NULL and leaves the file. An unlinked attachment is then served to any signed-in caller, including tokens restricted to other projects. Covered by [#4](https://github.com/parabol123654/velo/issues/4); the fix should also delete attachments of deleted issues.
10. **Session cookie is never `Secure`.** Behind an HTTPS proxy the cookie still travels over plain HTTP if a user opens an `http://` URL. Fix: set `Secure` when the request came over TLS, or carries `X-Forwarded-Proto: https` and the server runs with an opt-in `--trust-proxy` flag; with that flag, login throttling also uses `X-Forwarded-For`. The flag name and behavior need a decision recorded here first. Done when: server tests cover both headers with and without the flag.
11. **Version strings disagree.** `cli.Version` and the MCP `serverInfo` say `0.2.0` while migrations mention v0.3. Fix: one `Version` constant, set at build time with `-ldflags -X` in the Makefile, Dockerfile and release workflow, used by both. Done when: `velo version` and MCP `initialize` print the same tag.

## Next

Order for new work, after the known gaps. Issue links, `next_issue`/`claim_issue`, `check_item` and MCP resources/prompts shipped (#6, #12, #13, #14); close those issues if they are still open. Issues titled "Idea:" (#5, #7, #8) stay out until the discussion there ends with a decision.

1. [#2](https://github.com/parabol123654/velo/issues/2) Update cycles from the CLI and MCP.
2. [#17](https://github.com/parabol123654/velo/issues/17) Search syntax and saved filters.
3. [#4](https://github.com/parabol123654/velo/issues/4) Remove uploads left behind by deleted projects, together with known gap 9.
4. [#18](https://github.com/parabol123654/velo/issues/18), [#1](https://github.com/parabol123654/velo/issues/1), [#3](https://github.com/parabol123654/velo/issues/3) Release and test hygiene.
5. [#16](https://github.com/parabol123654/velo/issues/16) Imports from GitHub Issues, Jira and Linear.
6. [#15](https://github.com/parabol123654/velo/issues/15) `velo agent`, the largest item.

## Code map

```
cmd/velo/         entrypoint
internal/db/      schema, migrations, queries
internal/server/  HTTP API, auth, uploads, SSE hub, GitHub webhook
internal/cli/     CLI commands
internal/mcp/     MCP stdio server
internal/client/  HTTP client shared by CLI, MCP and the agent runner
internal/agent/   velo agent: runs a local coding agent on labeled issues
web/src/          React + TypeScript + Tailwind UI; live.ts holds the SSE reducer
```
