# ⚡ Velo

**Zero-bloat, single-binary, local-first Jira alternative built for developers and AI coding agents.**

 Jira was built in 2002 for enterprise middle-management. Modern open-source clones often require 8–12 Docker containers eating gigabytes of RAM just to move a card across a board.

**Velo** packs the entire stack—**REST API, real-time SSE, SQLite (`WAL` + `FTS5` full-text search), Linear-style React UI, CLI, and a native MCP (`stdio`) server**—into **one static binary (`~15–20 MB`)** using ~25 MB of RAM with zero external dependencies.

---

## ✨ Highlights

- **True Single Binary (`CGO_ENABLED=0`)**: Embedded React SPA + pure-Go SQLite (`modernc.org/sqlite`). Download or build once, run `./velo serve` anywhere (Linux, macOS, Windows, or a \$5 VPS).
- **Linear-Inspired Speed & Keyboard Shortcuts**:
  - `Cmd/Ctrl + K` or `/` — Command Palette & sub-2ms SQLite `FTS5` full-text search
  - `C` — Quick-create issue from anywhere
  - `J` / `K` + `Enter` — Navigate issue lists without touching the mouse
  - Dark & Light mode out of the box
- **Built-in AI Agent MCP Server (`velo mcp`)**: Native JSON-RPC 2.0 `stdio` Model Context Protocol server inside the same binary. When Claude Code, Cursor, or Antigravity updates a ticket via MCP, your open browser Kanban board moves cards **live in real time** over Server-Sent Events (SSE).
- **Lean Agile Domain**:
  - Automatic project issue identifiers (`DEV-1`, `DEV-2`, ...)
  - Kanban Board (drag-and-drop), List view, Backlog, Sub-issues, Labels, Priorities, and optional Sprints/Cycles
  - Markdown descriptions & comments with **`Ctrl+V` clipboard screenshot paste** and **interactive `- [ ]` checklists**
- **Zero Lock-in**: 1 SQLite file (`./data/velo.db`) + `./data/uploads/`, plus 1-click **Markdown (`.md`)** and **JSON (`.json`)** project export/import.

---

## 🚀 Quickstart (Clone & Build Local)

### Prerequisites
- **Go 1.23+**
- **Node.js 20+** & `npm`

### Build & Run in 3 Commands

```bash
git clone https://github.com/parabol123654/velo.git
cd velo
make build
./velo serve --addr :8080 --data ./data
```

Open **http://localhost:8080** — on first launch, Velo prompts you to create your initial Admin account (stored locally in `./data/velo.db`, 30-day `HttpOnly` session, zero `.env` or SMTP mail server setup required).

### Run with Docker

```bash
docker build -t velo .
docker run -d --name velo -p 8080:8080 -v $(pwd)/data:/data velo
```

---

## 🤖 Connect AI Coding Agents (MCP)

1. Open Velo Web UI → **Settings, Tokens & MCP** → **Generate Token** (`velo_...`).
2. Add Velo to your AI agent configuration (`claude_desktop_config.json`, `.cursor/mcp.json`, or Antigravity MCP settings):

```json
{
  "mcpServers": {
    "velo": {
      "command": "/absolute/path/to/velo",
      "args": ["mcp"],
      "env": {
        "VELO_URL": "http://127.0.0.1:8080",
        "VELO_TOKEN": "velo_your_personal_token"
      }
    }
  }
}
```

### Exposed MCP Tools
| Tool | Description |
| :--- | :--- |
| `list_projects` | List all projects with keys and open issue counts |
| `list_issues` | Filter issues by `project`, `state`, `category`, `priority`, or `query` |
| `get_issue` | Fetch full Markdown description, sub-issues, comments, and activity history |
| `create_issue` | Create an issue or sub-issue (`parent: "DEV-1"`) |
| `update_issue` | Move ticket across states (`"In Progress"`, `"In Review"`, `"Done"`), edit priority/body |
| `add_comment` | Post root-cause notes, test output, or commit summaries on an issue |
| `search_issues` | Instant SQLite FTS5 full-text search across identifiers, titles, and bodies |

---

## 💻 CLI Usage

```bash
# Save URL & token once to ~/.config/velo/config.json
./velo login --url http://127.0.0.1:8080 --token velo_xxx

# Manage projects
./velo project create DEV "Core Platform" --desc "Backend & infra"
./velo project list

# Manage issues from your terminal
./velo issue create --project DEV --title "Fix race in SSE hub" --priority 3 --label bug
./velo issue list --project DEV
./velo issue update DEV-1 --state "In Progress"
./velo issue comment DEV-1 "Reproduced locally; writing unit test."
./velo issue show DEV-1

# Instant FTS5 search
./velo search "SSE hub"
```

---

## 🧭 Engineering Principles

This codebase strictly follows the **[Karpathy Guidelines](./AGENTS.md)** (`.agents/skills/karpathy-guidelines/SKILL.md`):
1. **Think Before Coding** — Explicit assumptions, zero hidden magic.
2. **Simplicity First** — Direct `database/sql` queries (no ORM), Go standard library `net/http` mux, ~50-line SSE hub, minimal frontend dependencies.
3. **Surgical Changes** — Every file has a single clear responsibility.
4. **Goal-Driven Execution** — Automated tests across SQLite concurrency/FTS5, HTTP Auth/SSE/Uploads, and MCP `stdio` JSON-RPC (`make test`).

---

## 📄 License

Released under the [MIT License](./LICENSE).
