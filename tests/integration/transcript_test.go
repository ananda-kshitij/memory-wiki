// Package integration contains integration tests that exercise TranscriptStore
// against a real Postgres instance.
//
// Set DATABASE_URL before running; the tests are skipped otherwise.
//
//	DATABASE_URL=postgres://user:pass@localhost:5432/memwiki_test go test ./tests/integration/...
package integration

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Codex-AK/memory-wiki/internal/models"
	"github.com/Codex-AK/memory-wiki/internal/storage/db"
	"github.com/google/uuid"
)

var (
	conn  *sql.DB
	store *db.TranscriptStore
)

// TestMain runs migrations once before all tests in this package.
// If DATABASE_URL is not set the entire suite is skipped cleanly.
func TestMain(m *testing.M) {
	if os.Getenv("DATABASE_URL") == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL not set; skipping integration tests")
		os.Exit(0)
	}

	var err error
	conn, err = db.Connect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: db connect: %v\n", err)
		os.Exit(1)
	}

	if err := db.Migrate(conn); err != nil {
		fmt.Fprintf(os.Stderr, "integration: migrate: %v\n", err)
		os.Exit(1)
	}

	store = db.NewTranscriptStore(conn)
	code := m.Run()
	conn.Close()
	os.Exit(code)
}

// newTranscript returns a minimal Transcript ready to insert.
func newTranscript(content string) *models.Transcript {
	now := time.Now().UTC()
	return &models.Transcript{
		ID:        uuid.NewString(),
		Content:   content,
		Status:    models.StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// cleanup removes a transcript row when the test is done.
func cleanup(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := conn.Exec("DELETE FROM transcripts WHERE id = $1", id); err != nil {
			t.Logf("cleanup: failed to delete transcript %s: %v", id, err)
		}
	})
}

// ---------------------------------------------------------------------------

func TestCreate(t *testing.T) {
	tx := newTranscript("Hello world.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestCreateAndGetByID(t *testing.T) {
	tx := newTranscript("Integration test content.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.GetByID(tx.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("GetByID returned nil for existing row")
	}
	if got.ID != tx.ID {
		t.Errorf("ID: got %q, want %q", got.ID, tx.ID)
	}
	if got.Content != tx.Content {
		t.Errorf("Content: got %q, want %q", got.Content, tx.Content)
	}
	if got.Status != models.StatusPending {
		t.Errorf("Status: got %q, want %q", got.Status, models.StatusPending)
	}
}

func TestGetByIDNotFound(t *testing.T) {
	got, err := store.GetByID(uuid.NewString())
	if err != nil {
		t.Fatalf("GetByID for unknown ID: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for unknown ID, got %+v", got)
	}
}

func TestClaimPendingReturnsPending(t *testing.T) {
	tx := newTranscript("Claim me.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	claimed, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if claimed == nil {
		t.Fatal("ClaimPending returned nil; expected at least one pending transcript")
	}
	// The claimed transcript must be in "processing" status.
	if claimed.Status != models.StatusProcessing {
		t.Errorf("Status after claim: got %q, want %q", claimed.Status, models.StatusProcessing)
	}
}

func TestClaimPendingIdempotent(t *testing.T) {
	// Claiming the same transcript twice in the same DB session should yield it
	// only once (the second call should return a different one or nil).
	tx := newTranscript("Claim exactly once.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	first, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("first ClaimPending: %v", err)
	}
	if first == nil || first.ID != tx.ID {
		t.Fatalf("expected to claim tx %s, got %v", tx.ID, first)
	}

	// A second claim should NOT return the same transcript (it's now processing).
	second, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("second ClaimPending: %v", err)
	}
	if second != nil && second.ID == tx.ID {
		t.Error("second ClaimPending returned the already-processing transcript")
	}
}

func TestClaimPendingNoneAvailable(t *testing.T) {
	// Ensure no pending rows exist for this check by using a unique content marker.
	// (Other tests may have left rows, but they're all claimed/done by now in sequence.)
	// We cannot guarantee a clean slate, so just verify no panic / error on empty queue.
	_, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("ClaimPending on potentially empty queue: %v", err)
	}
}

func TestMarkDone(t *testing.T) {
	tx := newTranscript("Mark me done.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Claim first so the row is in processing state.
	_, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if err := store.MarkDone(tx.ID); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	got, err := store.GetByID(tx.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.StatusDone {
		t.Errorf("Status: got %q, want %q", got.Status, models.StatusDone)
	}
}

func TestMarkFailed(t *testing.T) {
	tx := newTranscript("Mark me failed.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.MarkFailed(tx.ID, "something went wrong"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	got, err := store.GetByID(tx.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.StatusFailed {
		t.Errorf("Status: got %q, want %q", got.Status, models.StatusFailed)
	}
	if got.Error != "something went wrong" {
		t.Errorf("Error field: got %q, want %q", got.Error, "something went wrong")
	}
}
