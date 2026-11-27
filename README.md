# Velo

A small, self-hosted issue tracker. One binary, one SQLite file, no external services.

Velo covers the parts of Jira most small teams actually use: projects, issues, sub-issues, a Kanban board, a backlog, labels, priorities and optional sprints (cycles). It also ships a CLI and an MCP server so coding agents can read and update issues.

## Features

- Single static binary (~12 MB). The web UI is embedded. Data lives in `./data/velo.db` plus `./data/uploads/`.
- Issue keys per project (`DEV-1`, `DEV-2`, ...), sub-issues, labels, priorities, cycles.
- Board, list and backlog views. Drag and drop on the board.
- Keyboard shortcuts: `Ctrl/Cmd+K` search, `C` new issue, `J`/`K` to move, `Enter` to open.
- Markdown descriptions and comments. Paste screenshots with `Ctrl+V`. Clickable `- [ ]` checklists.
- Full-text search (SQLite FTS5).
- Live updates in the browser via Server-Sent Events.
- Simple auth: first visit creates the admin, admins share invite links, API tokens for the CLI and MCP. No mail server needed.
- Account management in Settings: change your password; admins reset passwords, change roles, disable or delete users.
- Export a project to Markdown or JSON, import from JSON.

## Install

Download a binary from the [releases page](https://github.com/parabol123654/velo/releases), or build from source.

Requirements for building: Go 1.26+, Node.js 20+.

```bash
git clone https://github.com/parabol123654/velo.git
cd velo
make build
./velo serve --addr :8080 --data ./data
```

Open http://localhost:8080 and create the admin account.

With Docker:

```bash
docker build -t velo .
docker run -d -p 8080:8080 -v "$(pwd)/data:/data" velo
```

## CLI

Create an API token in Settings, then:

```bash
./velo login --url http://127.0.0.1:8080 --token velo_xxx

./velo project create DEV "Core Platform"
./velo issue create --project DEV --title "Fix login redirect" --priority 3 --label bug
./velo issue list --project DEV
./velo issue update DEV-1 --state "In Progress"
./velo issue comment DEV-1 "Reproduced locally."
./velo issue show DEV-1
./velo issue update DEV-1 --assignee me --cycle "Sprint 1"
./velo issue list --project DEV --assignee me
./velo project show DEV                 # states, labels, cycles
./velo project export DEV > DEV.md      # or --format json
./velo search "login"
```

`VELO_URL` and `VELO_TOKEN` environment variables override the saved config.

## MCP

`velo mcp` runs an MCP server over stdio. Example config for Claude Code, Cursor or similar:

```json
{
  "mcpServers": {
    "velo": {
      "command": "/path/to/velo",
      "args": ["mcp"],
      "env": {
        "VELO_URL": "http://127.0.0.1:8080",
        "VELO_TOKEN": "velo_xxx"
      }
    }
  }
}
```

Tools: `list_projects`, `get_project`, `list_users`, `list_issues`, `get_issue`, `create_issue`, `update_issue`, `delete_issue`, `next_issue`, `claim_issue`, `check_item`, `link_issues`, `unlink_issues`, `add_comment`, `search_issues`, `create_label`, `create_cycle`, `export_project`.

Assignees can be given as an email or `me`, cycles and labels by name, parents by identifier (`DEV-1`).

It also serves resources (`velo://issue/DEV-12`, `velo://project/DEV`) and prompts: `work_on_issue`, `split_issue`, `summarize_cycle`. A typical agent loop: `next_issue` → `claim_issue` → work → `check_item` for each acceptance criterion → `add_comment` → `update_issue` to In Review. Give each agent its own API token (Settings), limited to the projects it works on; its changes show "via <token name>".

Changes made through MCP or the CLI go through the same HTTP API as the web UI, so open boards update immediately.

## Backup

```bash
./velo backup --data ./data backup-$(date +%F).db   # safe while the server runs
cp -r ./data/uploads backup-uploads/                 # pasted images are plain files
```

With Docker: `docker exec <container> velo backup --data /data /data/backup.db`. To restore, stop Velo and put the backup in place as `data/velo.db`.

Do not copy `velo.db` alone while the server runs: recent writes may still be in `velo.db-wal`.

Older versions kept pasted images when their issue or project was deleted. To find and remove uploads nothing uses (files with no record, and uploads no issue or comment links; in both cases only those older than a day):

```bash
./velo uploads prune --data ./data           # lists what would be deleted
./velo uploads prune --data ./data --apply   # deletes it; safe while the server runs
```

With Docker: `docker exec <container> velo uploads prune --data /data`.

## Agent runner

`velo agent` hands issues to a coding agent on your machine. Add the label `agent` to an issue on the board and the runner picks it up:

```bash
export VELO_URL=http://127.0.0.1:8080 VELO_TOKEN=velo_xxx   # a token just for the agent
./velo agent --cmd 'claude -p {prompt}' --dir ~/code/myrepo
```

For each issue it claims the issue (assigned to you, moved to In Progress), runs the command with the `work_on_issue` prompt, and comments the exit code and the end of the output. `{prompt}` and `{issue}` are replaced inside arguments; without `{prompt}` the prompt is sent on stdin. The command runs without a shell, so issue text cannot inject commands. The command gets `VELO_URL`, `VELO_TOKEN` and `VELO_ISSUE`, so an agent with Velo's MCP server configured can tick checklist items and move the issue itself.

One issue at a time. A failed or timed-out run leaves the issue assigned so it is not retried in a loop. Useful flags: `--label` (default `agent`), `--project`, `--timeout` (default 1h), `--once`, `--dry-run`.

## GitHub webhook

Start Velo with a secret:

```bash
./velo serve --github-secret "some-long-random-string"
# or: VELO_GITHUB_WEBHOOK_SECRET=... ./velo serve
```

In your GitHub repo, go to Settings > Webhooks > Add webhook:

- Payload URL: `https://your-velo-host/api/webhooks/github`
- Content type: `application/json`
- Secret: the same string
- Events: "Pushes" and "Pull requests"

What it does:

- A commit mentioning `DEV-42` adds a comment on DEV-42 with a link to the commit.
- `Fixes DEV-42`, `Closes DEV-42` or `Resolves DEV-42` in a commit pushed to the default branch moves the issue to Done. On other branches it only comments.
- A pull request mentioning issues adds a comment when opened. When it is merged into the default branch, issues with a closing keyword in the title or body move to Done.
- If the commit author's email matches a Velo user, the comment is posted as that user. Otherwise it is posted as "GitHub".

Velo must be reachable from GitHub for this to work (a VPS, or a tunnel when running locally).

## Development

```bash
make test                  # Go tests
make test-web              # web tests (Vitest)
cd web && npm run dev      # UI dev server on :5173, proxies /api to :8080
```

Layout:

```
cmd/velo/         entrypoint
internal/db/      SQLite schema and queries (database/sql, no ORM)
internal/server/  HTTP API, auth, uploads, SSE
internal/cli/     CLI commands
internal/mcp/     MCP stdio server
internal/client/  HTTP client used by CLI and MCP
web/              React + TypeScript + Tailwind UI
```

See [CONTRIBUTING.md](./CONTRIBUTING.md) to get started. Contributors and coding agents should read [AGENTS.md](./AGENTS.md). The system is described in [SPEC.md](./SPEC.md), open work is in [GitHub Issues](https://github.com/parabol123654/velo/issues).

## License

MIT
