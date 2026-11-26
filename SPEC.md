# Velo spec

What Velo is and the rules it keeps. This describes the current system, not a plan; open work lives in [GitHub Issues](https://github.com/parabol123654/velo/issues). Contributor rules are in [AGENTS.md](./AGENTS.md).

## Goal

A self-hosted issue tracker for individuals and small dev teams (1–25 people) and their coding agents. It covers the part of Jira small teams actually use, and nothing else.

Non-goals: enterprise permission matrices, custom fields, workflow engines, mail servers, plugins, multi-tenant hosting.

## Invariants

These hold for every change. Breaking one needs an explicit decision, recorded here.

1. **One binary, one data directory.** The web UI is embedded with `embed.FS`. Data is `<data>/velo.db` (SQLite, WAL, FTS5) plus `<data>/uploads/`. No CGO (`modernc.org/sqlite`), no required external service.
2. **Plain SQL.** Queries live in `internal/db/` on `database/sql`. No ORM.
3. **One write path.** The web UI, the CLI and the MCP server all change data through the HTTP API, so validation is shared and every mutation reaches open browsers over SSE. The CLI and MCP never open the SQLite file.
4. **Schema changes are additive and automatic.** `migrate()` runs the `CREATE ... IF NOT EXISTS` schema, then adds columns that older databases lack (`addColumn`). A v0.1 database must open in any later version.
5. **Every change ships with a check:** `go test ./...`, `npm test` and `npm run build` in `web/`.

## Domain

| Concept | Rules |
| :-- | :-- |
| Project | Unique `key` of 2–10 uppercase letters or digits, starting with a letter (`DEV`). Issue numbers come from `issue_seq`, incremented in the same transaction as the insert, so keys never collide. |
| Workflow state | Per project, any name, but each belongs to one category: `backlog`, `unstarted`, `started`, `completed`, `canceled`. New projects get Backlog, Todo, In Progress, In Review, Done, Canceled. |
| Issue | `KEY-N` identifier, optional parent (sub-issues; deleting a parent detaches its children), Markdown description, priority 0–4 (none, low, medium, high, urgent), optional assignee and cycle, labels. |
| Issue link | Between two issues of the same project: `blocks`, `relates` (symmetric) or `duplicates`. The API also takes `blocked_by` and `duplicated_by` and stores the reversed form. An issue's `blocked_by_count` counts blockers not yet completed or canceled; finishing, reopening or deleting a blocker pushes `issue.updated` for the issues it blocks. Unlinking removes every link between the pair. |
| Ordering | `position REAL`. A drop between two cards takes the midpoint, so a move updates one row. |
| Cycle | Optional sprint with dates and status `upcoming`, `active`, `completed`. |
| Comment, activity, attachment | Comments are Markdown. Activities log field changes. Pasted images go to `uploads/` and are served only to signed-in users. |
| Search | FTS5 over identifier, title and description, kept in sync by triggers. |
| Export/import | Per project, Markdown (read-only) or JSON (round-trips through import). |

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
| `project.*`, `state.updated`, `cycle.*`, `user.*` | object | Debounced full workspace reload |

Each event belongs to a project (user events to none); a project-restricted token's stream only gets its projects' events. The board logic is the pure function `applyEvent` in `web/src/live.ts`. A slow client may miss events (the hub drops them when its buffer is full); the UI reloads once whenever the stream reconnects.

## Integrations

- **CLI:** `velo serve | login | project | issue | label | cycle | user | search | mcp | version`. `VELO_URL` and `VELO_TOKEN` override the saved config.
- **MCP (stdio):** tools `list_projects`, `get_project`, `list_users`, `list_issues`, `get_issue`, `create_issue`, `update_issue`, `delete_issue`, `link_issues`, `unlink_issues`, `add_comment`, `search_issues`, `create_label`, `create_cycle`, `export_project`.
- **Friendly references:** wherever the API takes an assignee it also accepts an email or `me`; cycles accept a name; parent filters accept an identifier. Disabled users cannot be assigned.
- **GitHub webhook:** `POST /api/webhooks/github`, enabled by `--github-secret` or `VELO_GITHUB_WEBHOOK_SECRET`. Commits and PRs that mention `KEY-N` get a comment. `Fixes`, `Closes` or `Resolves KEY-N` on the default branch (or a PR merged into it) moves the issue to the first completed state. Comments are posted as the user with the matching email, otherwise as a built-in "GitHub" user that cannot log in.

## Code map

```
cmd/velo/         entrypoint
internal/db/      schema, migrations, queries
internal/server/  HTTP API, auth, uploads, SSE hub, GitHub webhook
internal/cli/     CLI commands
internal/mcp/     MCP stdio server
internal/client/  HTTP client shared by CLI and MCP
web/src/          React + TypeScript + Tailwind UI; live.ts holds the SSE reducer
```
