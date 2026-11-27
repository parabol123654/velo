# Velo

A small, self-hosted issue tracker for individuals, small teams and their coding agents. One binary (~12 MB, web UI embedded), one SQLite file, no external services.

- Projects with `DEV-1` style keys, sub-issues, labels, priorities, cycles (sprints) and `blocks` links.
- Board with drag and drop, list and backlog views, live updates. Shortcuts: `Ctrl/Cmd+K` search, `C` new issue, `J`/`K` move, `Enter` open.
- One-line filters, saved per user: `assignee:me is:open label:bug priority:>=high login`. Full-text search ignores accents (`dang nhap` finds `đăng nhập`).
- Markdown with pasted screenshots and clickable `- [ ]` checklists.
- A CLI, an MCP server and an agent runner, so coding agents work on the same board as people.
- First visitor becomes admin, others join by invite link. API tokens can be read-only or limited to projects. No mail server.
- Import from GitHub Issues, Jira or Linear; export a project to Markdown or JSON.

[SPEC.md](./SPEC.md) describes every behavior in detail.

## Run

From a [release binary](https://github.com/parabol123654/velo/releases):

```bash
./velo serve --addr :8080 --data ./data
```

From source (Go 1.26+, Node.js 20+, GNU Make):

```bash
git clone https://github.com/parabol123654/velo.git && cd velo
make build && ./velo serve --data ./data
```

With Docker (`linux/amd64` and `linux/arm64` images from v0.4.0 on; `latest` is the newest full release):

```bash
docker run -d -p 8080:8080 -v "$(pwd)/data:/data" ghcr.io/parabol123654/velo:latest
# or build it: docker build --build-arg VERSION=$(git describe --tags --always) -t velo .
```

Open http://localhost:8080 and create the admin account. Data lives in `data/velo.db` and `data/uploads/`. `--trust-proxy` and `--github-secret` can also be set with `VELO_TRUST_PROXY=1` and `VELO_GITHUB_WEBHOOK_SECRET`, which is handy in Docker.

**Behind a reverse proxy** that terminates HTTPS, add `--trust-proxy`: the session cookie becomes `Secure` and failed logins are counted per client. The proxy must set `X-Forwarded-Proto` and `X-Forwarded-For` (Caddy does; in nginx, `proxy_set_header X-Forwarded-Proto $scheme;` and `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`). Leave it off when clients reach Velo directly, since anyone can send these headers. Details: [SPEC.md](./SPEC.md#accounts-and-access).

## CLI

Create an API token in Settings, then:

```bash
./velo login --url http://127.0.0.1:8080 --token velo_xxx
./velo project create DEV "Core Platform"
./velo issue create --project DEV --title "Fix login redirect" --priority 3 --label bug
./velo issue update DEV-1 --state "In Progress" --assignee me
./velo issue comment DEV-1 "Reproduced locally."
./velo search 'assignee:me is:open priority:>=high login'
```

Run `./velo` with no arguments for every command. `VELO_URL` and `VELO_TOKEN` override the saved config.

## Import from GitHub, Jira or Linear

Create the project first, then:

```bash
./velo import github OWNER/REPO --project DEV --dry-run      # see what would happen
./velo import github OWNER/REPO --project DEV --map-user octocat=an@example.com
./velo import jira jira-export.csv --project DEV --state-map "QA=In Review"
./velo import linear linear-export.csv --project DEV
```

The import runs on your machine and goes through the API, so a GitHub token (`GITHUB_TOKEN` or `--github-token`) never reaches Velo. Issues get new `DEV-N` numbers and a line linking to the original; labels, cycles (milestones, sprints), parents, comments and links come along. Running it again skips what is already imported and adds only what is new. For Jira use "Export CSV (all fields)". What maps to what: [SPEC.md](./SPEC.md#integrations).

## Coding agents

Give each agent its own API token (Settings), limited to the projects it works on. Its changes show "via <token name>", and every change reaches open boards at once, because the CLI and MCP server go through the same HTTP API as the web UI.

**MCP.** `velo mcp` is an MCP server over stdio. For Claude Code, Cursor and similar:

```json
{
  "mcpServers": {
    "velo": {
      "command": "/path/to/velo",
      "args": ["mcp"],
      "env": { "VELO_URL": "http://127.0.0.1:8080", "VELO_TOKEN": "velo_xxx" }
    }
  }
}
```

A typical loop: `next_issue` → `claim_issue` → work → `check_item` per acceptance criterion → `add_comment` → `update_issue` to In Review. The `work_on_issue` prompt walks an agent through it. Tools, resources and prompts: [SPEC.md](./SPEC.md#integrations).

**Agent runner.** `velo agent` runs a coding agent on your machine for each issue labeled `agent`:

```bash
VELO_URL=http://127.0.0.1:8080 VELO_TOKEN=velo_xxx ./velo agent --cmd 'claude -p {prompt}' --dir ~/code/myrepo
```

It claims the issue, runs the command (without a shell; `{prompt}` and `{issue}` are filled in, or the prompt goes to stdin) and comments the exit code and the end of the output. The command gets `VELO_URL`, `VELO_TOKEN` and `VELO_ISSUE`, so an agent with the MCP server can tick checklist items and move the issue itself. One issue at a time; a failed run stays assigned and is not retried. Flags: `--label`, `--project`, `--timeout` (1h), `--once`, `--dry-run`.

## GitHub webhook

Start Velo with `--github-secret <secret>`. In the GitHub repo, add a webhook (Settings > Webhooks) with payload URL `https://<your-velo-host>/api/webhooks/github`, content type `application/json`, the same secret, and the events "Pushes" and "Pull requests". Velo must be reachable from GitHub.

Commits and pull requests that mention `DEV-42` get a comment on DEV-42. `Fixes`, `Closes` or `Resolves DEV-42` on the default branch, or in a pull request merged into it, moves the issue to Done.

## Backup

```bash
./velo backup --data ./data backup-$(date +%F).db   # safe while the server runs
cp -r ./data/uploads backup-uploads/
```

Don't copy `velo.db` alone while the server runs: recent writes may still be in `velo.db-wal`. To restore, stop Velo and put the backup in place as `data/velo.db`. In Docker: `docker exec <container> velo backup --data /data /data/backup.db`.

Older versions kept uploads after their issue was deleted. `./velo uploads prune --data ./data` lists unused uploads older than a day; add `--apply` to delete them.

## Development

```bash
make check                  # what CI runs: build, vet, gofmt, Go and web tests
make race                   # go test -race (needs a C compiler)
cd web && npm run dev       # UI on :5173, proxies /api to a running ./velo serve on :8080
```

Start with [CONTRIBUTING.md](./CONTRIBUTING.md) (setup, rules, pull requests). The code map is in [SPEC.md](./SPEC.md#code-map); coding agents also read [AGENTS.md](./AGENTS.md). Open work is in [GitHub Issues](https://github.com/parabol123654/velo/issues), security reports go through [SECURITY.md](./SECURITY.md).

## License

MIT
