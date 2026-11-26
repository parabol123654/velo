# Velo — Engineering & AI Agent Guidelines

All human and AI contributors working on `velo` MUST follow the **Karpathy Guidelines** (also located in `.agents/skills/karpathy-guidelines/SKILL.md`):

1. **Think Before Coding:** Surface assumptions and tradeoffs explicitly. Never hide ambiguity.
2. **Simplicity First:** Minimum code that solves the problem. Zero speculative abstractions, zero unused configurability, zero heavy ORMs or bloated dependencies. If 200 lines can be 50, rewrite it.
3. **Surgical Changes:** Touch only what is required for the task. Keep changes traceable and clean up orphans you create.
4. **Goal-Driven Execution:** Every change must be paired with verifiable automated checks (`go test ./...`, `npm run build`).

## Architectural Invariants

- **Single Binary & Zero External Dependencies:** `velo` embeds the compiled frontend (`web/dist`) via Go `embed.FS` and uses pure-Go SQLite (`modernc.org/sqlite`) in `WAL` mode with `FTS5` search. Never introduce CGO or mandatory external services (Redis, Postgres, Kafka).
- **Direct SQL over ORMs:** Keep queries explicit in `internal/db/` using standard `database/sql`.
- **Unified Validation & Live SSE:** Both the Web UI and `velo` CLI / MCP (`stdio`) mutate state through the HTTP API so every change triggers SSE (`internal/server/sse.go`) for live board updates.
