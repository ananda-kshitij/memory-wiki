# Memory Wiki

A service that ingests conversation transcripts, uses Claude to extract memories, and stores them as a navigable file tree in object storage — exposed via unix-style REST endpoints.

## Architecture

```
POST /transcripts
       │
       ▼
  [PostgreSQL]  ← status: pending → processing → done | failed
       │              attempts counter + retry_after (exponential backoff)
       ▼
  Background worker (polls every 5s, FOR UPDATE SKIP LOCKED)
       │
       ▼
  Claude API (claude-opus-4-8)
       │  extracts structured memory entries
       ▼
  MinIO object store                     pgvector (optional)
       └── memories/              ──►    memory_embeddings table
           ├── people/alice.md           (OpenAI text-embedding-3-small)
           ├── topics/machine-learning.md
           └── projects/phoenix.md
```

Each memory file has YAML frontmatter:
```yaml
---
tags: [ml, research]
last_updated: 2026-06-25T...
source_transcript_ids: [uuid1, uuid2]
---
# Content in markdown
```

### Key decisions

**Memory tree (Approach B — topic/category flat files)**: Files grouped by semantic category (`people/`, `topics/`, `projects/`, `events/`, `preferences/`). New transcript insights are _upserted_ into existing files rather than creating new ones per transcript, so memories accumulate over time.

**Background processing (Option 2 — DB status polling)**: A worker polls for `pending` transcripts using `SELECT ... FOR UPDATE SKIP LOCKED` for safe concurrent processing. Status fields (`pending → processing → done | failed`) make job state inspectable via the GET endpoint without a separate queue infrastructure.

**Retry logic with exponential backoff**: Failed transcripts are re-queued to `pending` up to 3 attempts. Backoff delays are `5s * 2^(attempt-1)` — so 5s after the first failure, 10s after the second, then permanently `failed`. The `retry_after` column prevents workers from picking up a job before the backoff window expires.

**Semantic search (optional)**: On every memory write, an embedding is generated via OpenAI `text-embedding-3-small` (1536 dims) and stored in a `memory_embeddings` pgvector table. `GET /memories/search?q=...&mode=semantic` runs a cosine similarity query instead of a brute-force substring scan. If `OPENAI_API_KEY` is not set, the endpoint silently falls back to keyword search — no configuration required to run the app.

## Running locally

### Prerequisites
- [Docker Desktop](https://www.docker.com/products/docker-desktop/)
- An [Anthropic API key](https://console.anthropic.com/)
- (Optional) An [OpenAI API key](https://platform.openai.com/) — enables semantic search; keyword search is used without it

### Steps

```bash
# 1. Copy env file and fill in your keys
cp .env.example .env
# Required: ANTHROPIC_API_KEY
# Optional: OPENAI_API_KEY  (enables semantic search)

# 2. Start everything
docker compose up --build

# 3. Ingest a transcript
curl -X POST http://localhost:8080/transcripts \
  -H 'Content-Type: application/json' \
  -d '{"content": "Alice mentioned she loves hiking and works on ML research at Stanford."}'

# Response: {"id": "uuid", "status": "pending", ...}

# 4. Poll status
curl http://localhost:8080/transcripts/<id>

# 5. Browse memories (once status is "done")
curl http://localhost:8080/memories
curl http://localhost:8080/memories?prefix=people/
curl http://localhost:8080/memories/people/alice
curl 'http://localhost:8080/memories/search?q=Stanford'

# Semantic search (requires OPENAI_API_KEY in .env)
curl 'http://localhost:8080/memories/search?q=machine+learning&mode=semantic'
```

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| POST | `/transcripts` | Ingest a transcript (body: `{"content": "..."}`) |
| GET | `/transcripts/:id` | Poll transcript status |
| GET | `/memories?prefix=` | List memory files (unix `ls`) |
| GET | `/memories/:category/:name` | Read a memory file (unix `cat`) |
| GET | `/memories/search?q=&mode=` | Search memories — keyword (default) or `mode=semantic` for vector similarity |

## Development (without Docker)

```bash
# Start only Postgres + MinIO
docker compose up db minio

# Run the server directly
go run ./cmd/server
```

## Tech Stack

- **Go** — HTTP server (chi router), worker, business logic
- **PostgreSQL 16 + pgvector** — transcript storage, job status, and vector embeddings
- **MinIO** — S3-compatible object store for memory files
- **Anthropic Claude** (`claude-opus-4-8`) — memory extraction and reconciliation
- **OpenAI** (`text-embedding-3-small`, optional) — semantic search embeddings

## Testing

The project has a three-tier test pyramid:

| Layer | Command | What it tests |
|---|---|---|
| Unit | `go test ./internal/...` | Business logic with mocked dependencies — 93.9% coverage on `memory/store` |
| Integration | `DATABASE_URL=... go test ./tests/integration/...` | `TranscriptStore` against real Postgres — full retry cycle, backoff ordering, `retry_after` gating |
| E2E | `DATABASE_URL=... MINIO_ENDPOINT=... go test ./tests/e2e/...` | Full pipeline with real Postgres + MinIO; verifies file physically exists in MinIO and DB row reaches `status=done` |

No Anthropic API key is needed for any test — the E2E suite uses a fake LLM client.

## What I Would Have Done With More Time

**Auth and multi-tenancy.** There's no auth today — all memories are global. A real deployment would scope memories per user or organization with JWT or API key authentication, which would also require schema changes to partition the transcript table and the object storage key namespace.

**Streaming LLM responses.** For long transcripts, the current blocking `Messages.New` call holds the worker goroutine until the full response arrives. Switching to the streaming API would allow earlier error detection and make it practical to surface incremental progress on the transcript status endpoint.

**Rate limiting and observability.** The ingest endpoint has no rate limiting, which makes it trivial to exhaust the Anthropic API quota. On the observability side there's no structured logging, no Prometheus metrics, and no distributed tracing — all of which would be table stakes before running this in production.
