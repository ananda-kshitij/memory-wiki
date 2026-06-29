// Package e2e contains end-to-end tests that exercise the full system:
// HTTP server → worker → real Postgres → real MinIO.
//
// Prerequisites (skip if absent):
//   - DATABASE_URL env var pointing to a Postgres instance
//   - MINIO_ENDPOINT, MINIO_ACCESS_KEY, MINIO_SECRET_KEY, MINIO_BUCKET env vars
//
// The LLM call is replaced with a fake client so no Anthropic API key is needed.
//
//	DATABASE_URL=... MINIO_ENDPOINT=... go test ./tests/e2e/... -v -timeout 60s
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/go-chi/chi/v5"

	"github.com/Codex-AK/memory-wiki/internal/api/handler"
	"github.com/Codex-AK/memory-wiki/internal/models"
	memstore "github.com/Codex-AK/memory-wiki/internal/memory"
	"github.com/Codex-AK/memory-wiki/internal/storage/db"
	"github.com/Codex-AK/memory-wiki/internal/storage/object"
	"github.com/Codex-AK/memory-wiki/internal/worker"
)

// ---------------------------------------------------------------------------
// Fake LLM — returns deterministic memory entries without calling Anthropic.
// ---------------------------------------------------------------------------

type fakeLLM struct {
	entries []models.MemoryEntry
}

func (f *fakeLLM) ExtractMemories(_ context.Context, _ string) ([]models.MemoryEntry, error) {
	return f.entries, nil
}

// fakeReconciler appends new content to the existing file body (no LLM call).
type fakeReconciler struct{}

func (fakeReconciler) ReconcileMemory(_ context.Context, existing string, entry models.MemoryEntry, _ string) (string, error) {
	return existing + "\n\n---\n\n" + entry.Content, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func skipUnlessEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set; skipping e2e test")
	}
	if os.Getenv("MINIO_ENDPOINT") == "" {
		t.Skip("MINIO_ENDPOINT not set; skipping e2e test")
	}
}

type e2eFixture struct {
	conn            *db.TranscriptStore
	objClient       *object.Client
	memStore        *memstore.Store
	srv             *httptest.Server
	rawConn         interface{ Exec(string, ...interface{}) (interface{}, error) }
}

// ---------------------------------------------------------------------------
// E2E: full transcript → memory pipeline
// ---------------------------------------------------------------------------

// TestE2ETranscriptToMemory posts a transcript, waits for the worker to
// process it, then verifies the resulting memory file is visible through the
// API and physically present in MinIO.
func TestE2ETranscriptToMemory(t *testing.T) {
	skipUnlessEnv(t)

	conn, err := db.Connect()
	if err != nil {
		t.Fatalf("db connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	if err := db.Migrate(conn); err != nil {
		t.Fatalf("db migrate: %v", err)
	}

	objClient, err := object.New()
	if err != nil {
		t.Fatalf("object store init: %v", err)
	}

	transcriptStore := db.NewTranscriptStore(conn)
	memStore := memstore.NewStore(objClient, fakeReconciler{})

	fake := &fakeLLM{
		entries: []models.MemoryEntry{
			{
				Category: "people",
				Name:     "e2e-test-subject",
				Tags:     []string{"e2e", "automated"},
				Content:  "A person created during the automated end-to-end test.",
			},
		},
	}

	w := worker.New(transcriptStore, fake, memStore)
	w.SetInterval(100 * time.Millisecond)

	workerCtx, stopWorker := context.WithCancel(context.Background())
	t.Cleanup(stopWorker)
	go w.Run(workerCtx)

	r := chi.NewRouter()
	r.Use(chimiddleware.Recoverer)

	th := handler.NewTranscriptHandler(transcriptStore)
	mh := handler.NewMemoryHandler(memStore)

	r.Post("/transcripts", th.Create)
	r.Get("/transcripts/{id}", th.Get)
	r.Get("/memories", mh.Ls)
	r.Get("/memories/search", mh.Grep)
	r.Get("/memories/{category}/{name}", mh.Cat)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	// Step 1: POST a transcript.
	body := `{"content": "Alice is an engineer who contributed to the e2e test suite."}`
	resp, err := http.Post(srv.URL+"/transcripts", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /transcripts: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /transcripts: got status %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	var txResp models.Transcript
	if err := json.NewDecoder(resp.Body).Decode(&txResp); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if txResp.ID == "" {
		t.Fatal("transcript ID is empty in response")
	}
	t.Logf("created transcript %s", txResp.ID)

	t.Cleanup(func() {
		conn.Exec("DELETE FROM transcripts WHERE id = $1", txResp.ID)
	})

	// Step 2: Poll until status is "done".
	pollTimeout := 15 * time.Second
	deadline := time.Now().Add(pollTimeout)
	var finalStatus models.TranscriptStatus

	for time.Now().Before(deadline) {
		r, err := http.Get(fmt.Sprintf("%s/transcripts/%s", srv.URL, txResp.ID))
		if err != nil {
			t.Fatalf("GET /transcripts/%s: %v", txResp.ID, err)
		}
		var poll models.Transcript
		_ = json.NewDecoder(r.Body).Decode(&poll)
		r.Body.Close()

		finalStatus = poll.Status
		t.Logf("poll: status=%s", finalStatus)

		if finalStatus == models.StatusDone || finalStatus == models.StatusFailed {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if finalStatus != models.StatusDone {
		t.Fatalf("transcript %s: final status %q, want %q (timed out after %s)",
			txResp.ID, finalStatus, models.StatusDone, pollTimeout)
	}

	wantKey := "memories/people/e2e-test-subject.md"

	// Step 3: GET /memories and verify the file appears.
	memResp, err := http.Get(srv.URL + "/memories")
	if err != nil {
		t.Fatalf("GET /memories: %v", err)
	}
	defer memResp.Body.Close()

	if memResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /memories: status %d", memResp.StatusCode)
	}

	var files struct {
		Files []string `json:"files"`
	}
	if err := json.NewDecoder(memResp.Body).Decode(&files); err != nil {
		t.Fatalf("decode GET /memories response: %v", err)
	}

	if len(files.Files) == 0 {
		t.Fatal("expected at least one memory file after processing, got none")
	}

	found := false
	for _, f := range files.Files {
		if f == wantKey {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %q in /memories response, got %v", wantKey, files.Files)
	}

	// Step 4: GET /memories/{category}/{name} and verify content.
	catResp, err := http.Get(srv.URL + "/memories/people/e2e-test-subject")
	if err != nil {
		t.Fatalf("GET /memories/people/e2e-test-subject: %v", err)
	}
	defer catResp.Body.Close()

	if catResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /memories/people/e2e-test-subject: status %d", catResp.StatusCode)
	}

	// Step 5: GET /memories/search?q=e2e and verify the file shows up.
	searchResp, err := http.Get(srv.URL + "/memories/search?q=e2e")
	if err != nil {
		t.Fatalf("GET /memories/search: %v", err)
	}
	defer searchResp.Body.Close()

	var searchResult struct {
		Matches []string `json:"matches"`
	}
	if err := json.NewDecoder(searchResp.Body).Decode(&searchResult); err != nil {
		t.Fatalf("decode search response: %v", err)
	}

	searchFound := false
	for _, m := range searchResult.Matches {
		if m == wantKey {
			searchFound = true
			break
		}
	}
	if !searchFound {
		t.Errorf("search for 'e2e' did not return %q; matches: %v", wantKey, searchResult.Matches)
	}

	// Step 6: Verify the file physically exists in MinIO (object storage).
	data, err := objClient.Get(context.Background(), wantKey)
	if err != nil {
		t.Fatalf("MinIO Get %q: %v — file not found in object storage", wantKey, err)
	}
	if len(data) == 0 {
		t.Error("MinIO file exists but is empty")
	}
	content := string(data)
	if !strings.Contains(content, "e2e-test-subject") && !strings.Contains(content, "automated") {
		t.Errorf("MinIO file content looks wrong: %q", content)
	}
	t.Logf("MinIO object %q verified (%d bytes)", wantKey, len(data))

	// Step 7: Verify the transcript row exists in Postgres with status=done.
	dbRow, err := db.NewTranscriptStore(conn).GetByID(txResp.ID)
	if err != nil {
		t.Fatalf("GetByID from DB: %v", err)
	}
	if dbRow == nil {
		t.Fatal("transcript row not found in Postgres")
	}
	if dbRow.Status != models.StatusDone {
		t.Errorf("DB transcript status: got %q, want done", dbRow.Status)
	}
	t.Logf("Postgres row %q verified (status=%s, attempts=%d)", dbRow.ID, dbRow.Status, dbRow.Attempts)
}

// ---------------------------------------------------------------------------
// E2E: handler validation
// ---------------------------------------------------------------------------

func TestPostTranscriptEmptyContent(t *testing.T) {
	skipUnlessEnv(t)

	conn, err := db.Connect()
	if err != nil {
		t.Fatalf("db connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = db.Migrate(conn)

	r := chi.NewRouter()
	th := handler.NewTranscriptHandler(db.NewTranscriptStore(conn))
	r.Post("/transcripts", th.Create)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	for _, body := range []string{`{"content": ""}`, `{"content": "   "}`, `{}`} {
		resp, err := http.Post(srv.URL+"/transcripts", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: got status %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestSearchWithoutQuery(t *testing.T) {
	skipUnlessEnv(t)

	conn, err := db.Connect()
	if err != nil {
		t.Fatalf("db connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = db.Migrate(conn)

	objClient, err := object.New()
	if err != nil {
		t.Fatalf("object store: %v", err)
	}

	r := chi.NewRouter()
	mh := handler.NewMemoryHandler(memstore.NewStore(objClient, fakeReconciler{}))
	r.Get("/memories/search", mh.Grep)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/memories/search")
	if err != nil {
		t.Fatalf("GET /memories/search: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got status %d, want 400 when q is missing", resp.StatusCode)
	}
}

func TestCatNotFound(t *testing.T) {
	skipUnlessEnv(t)

	objClient, err := object.New()
	if err != nil {
		t.Fatalf("object store: %v", err)
	}

	r := chi.NewRouter()
	mh := handler.NewMemoryHandler(memstore.NewStore(objClient, fakeReconciler{}))
	r.Get("/memories/{category}/{name}", mh.Cat)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/memories/people/definitely-does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("got status %d, want 404 for non-existent memory", resp.StatusCode)
	}
}
