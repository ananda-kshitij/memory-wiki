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
  MinIO object store                     pgvector
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

**Memory storage layout**

Three approaches were considered:
- **Per-transcript files** — one file per ingested transcript, e.g. `transcripts/2026-06-25-uuid.md`. Simple to write but useless for retrieval: finding everything known about Alice requires scanning every file.
- **Per-entity files, one file per mention** — a new file for every entity extracted from every transcript. Keeps files small but creates unbounded duplication; Alice across ten conversations becomes ten files with no single source of truth.
- **Per-entity files, upserted** (Selected) — one canonical file per named entity (e.g. `people/alice.md`), updated in place each time a new transcript mentions that entity. Memories accumulate over time in a single, navigable location. This is the approach used.

Files are grouped by semantic category (`people/`, `topics/`, `projects/`, `events/`, `preferences/`) and carry YAML frontmatter (`tags`, `last_updated`, `source_transcript_ids`) so provenance is always traceable.

---

**Background processing**

Three options were considered:
- **Synchronous processing** — process the transcript inline during the POST request and return the result. Simple, but a 10-second LLM call blocks the HTTP connection and makes the API fragile under load.
- **External message queue (SQS, RabbitMQ, etc.)** — reliable and scalable, but introduces a new infrastructure dependency that complicates local setup and deployment.
- **DB-backed polling worker** (Selected)  — a background goroutine polls for `pending` transcripts using `SELECT ... FOR UPDATE SKIP LOCKED`, which prevents two workers from claiming the same job. Status fields (`pending → processing → done | failed`) are queryable via the GET endpoint, so callers can poll progress without a separate notification mechanism. No extra infrastructure beyond Postgres.

---

**Retry logic with exponential backoff**

When an LLM call fails transiently (network error, rate limit, etc.), there are two naive options: fail permanently (loses work silently) or retry immediately in a tight loop (hammers the API). Neither is acceptable.

The chosen approach: failed transcripts are re-queued to `pending` up to 3 attempts with exponential backoff — `5s * 2^(attempt-1)`, so 5s after attempt 1, 10s after attempt 2, then permanently `failed`. A `retry_after` timestamp column prevents workers from picking up the job before the backoff window expires, and the `attempts` counter makes the retry history visible in the GET response.

---

**Semantic search**

Two search approaches were considered:
- **Keyword scan** — iterate every file in MinIO and check for substring matches. Zero infrastructure cost but O(n) in file count and blind to synonyms or paraphrasing (`"ML researcher"` won't match `"machine learning scientist"`).
- **Vector similarity search (pgvector)** (Selected) — on every memory write, generate an embedding via OpenAI `text-embedding-3-small` (1536 dims) and upsert it into a `memory_embeddings` table. `GET /memories/search?q=...&mode=semantic` runs a cosine similarity query (`<=>` operator) instead of a file scan, returning semantically related results regardless of exact wording.

To keep the app runnable without an OpenAI account, the embedder is optional: if `OPENAI_API_KEY` is not set, semantic mode silently falls back to keyword search.

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

## CI/CD

Every push and pull request runs the full test suite automatically via GitHub Actions (`.github/workflows/ci.yml`). The pipeline:

1. Starts Postgres (`pgvector/pgvector:pg16`) and MinIO as ephemeral services
2. Builds the project (`go build ./...`)
3. Runs unit → integration → E2E tests in sequence
4. Prints a combined coverage summary

The `main` branch is protected — pull requests can only be merged once the `Test` job is green, ensuring no broken code lands in the main branch.

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
