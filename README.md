# Memory Wiki

A service that ingests conversation transcripts, uses Claude to extract memories, and stores them as a navigable file tree in object storage — exposed via unix-style REST endpoints.

## Architecture

```
POST /transcripts
       │
       ▼
  [PostgreSQL]  ← status: pending → processing → done | failed
       │
       ▼
  Background worker (polls every 5s, FOR UPDATE SKIP LOCKED)
       │
       ▼
  Claude API (claude-opus-4-8)
       │  extracts structured memory entries
       ▼
  MinIO object store
       └── memories/
           ├── people/alice.md
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

## Running locally

### Prerequisites
- [Docker Desktop](https://www.docker.com/products/docker-desktop/)
- An [Anthropic API key](https://console.anthropic.com/)

### Steps

```bash
# 1. Copy env file and add your API key
cp .env.example .env
# Edit .env and set ANTHROPIC_API_KEY

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
```

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| POST | `/transcripts` | Ingest a transcript (body: `{"content": "..."}`) |
| GET | `/transcripts/:id` | Poll transcript status |
| GET | `/memories?prefix=` | List memory files (unix `ls`) |
| GET | `/memories/:category/:name` | Read a memory file (unix `cat`) |
| GET | `/memories/search?q=` | Full-text search across all memories (unix `grep`) |

## Development (without Docker)

```bash
# Start only Postgres + MinIO
docker compose up db minio

# Run the server directly
go run ./cmd/server
```

## Tech Stack

- **Go** — HTTP server (chi router), worker, business logic
- **PostgreSQL** — transcript storage and job status tracking
- **MinIO** — S3-compatible object store for memory files
- **Anthropic Claude** (`claude-opus-4-8`) — memory extraction

## What I Would Have Done With More Time

**Smarter memory reconciliation.** Right now new transcript content is appended to existing memory files with a `---` separator. A better approach would be to pass the existing file content back to the LLM alongside the new transcript and ask it to produce a single coherent, deduplicated, updated version — rather than accumulating raw sections that grow without bound.

**Retry logic with backoff.** Failed transcripts currently stay in `failed` state permanently. A proper implementation would add an `attempts` counter column, re-queue failed jobs to `pending` up to a configurable limit (e.g. 3), and apply exponential backoff between retries so transient API errors don't silently drop work.

**Semantic search / embeddings.** The current grep endpoint is a brute-force substring scan over every file — it doesn't scale and misses semantically related content that doesn't share exact keywords. With more time I'd generate embeddings for each memory file on write and store them in pgvector, turning the search endpoint into a meaningful similarity query.

**Auth and multi-tenancy.** There's no auth today — all memories are global. A real deployment would scope memories per user or organization with JWT or API key authentication, which would also require schema changes to partition the transcript table and the object storage key namespace.

**Streaming LLM responses.** For long transcripts, the current blocking `Messages.New` call holds the worker goroutine until the full response arrives. Switching to the streaming API would allow earlier error detection and make it practical to surface incremental progress on the transcript status endpoint.

**More test coverage.** The existing unit tests mock storage and the LLM client, which is fast but can't catch integration-level regressions. I'd add tests that run against real Postgres and MinIO (straightforward with `testcontainers-go`), and property-based tests for the YAML frontmatter parse/render round-trip since that's the most error-prone serialization boundary.

**Rate limiting and observability.** The ingest endpoint has no rate limiting, which makes it trivial to exhaust the Anthropic API quota. On the observability side there's no structured logging, no Prometheus metrics, and no distributed tracing — all of which would be table stakes before running this in production.
