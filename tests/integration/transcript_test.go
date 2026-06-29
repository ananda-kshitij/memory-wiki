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
// Basic CRUD
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

// ---------------------------------------------------------------------------
// ClaimPending
// ---------------------------------------------------------------------------

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
	if claimed.Status != models.StatusProcessing {
		t.Errorf("Status after claim: got %q, want %q", claimed.Status, models.StatusProcessing)
	}
}

func TestClaimPendingIdempotent(t *testing.T) {
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

	second, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("second ClaimPending: %v", err)
	}
	if second != nil && second.ID == tx.ID {
		t.Error("second ClaimPending returned the already-processing transcript")
	}
}

func TestClaimPendingNoneAvailable(t *testing.T) {
	_, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("ClaimPending on potentially empty queue: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MarkDone
// ---------------------------------------------------------------------------

func TestMarkDone(t *testing.T) {
	tx := newTranscript("Mark me done.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}
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
	if got.RetryAfter != nil {
		t.Errorf("RetryAfter should be nil after MarkDone, got %v", got.RetryAfter)
	}
}

// ---------------------------------------------------------------------------
// MarkFailed + retry logic
// ---------------------------------------------------------------------------

// TestMarkFailedRequeues verifies that failing a transcript on the first attempt
// sets status back to 'pending' (not 'failed') with a retry_after backoff.
func TestMarkFailedRequeues(t *testing.T) {
	tx := newTranscript("Fail me once.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Claim once → attempts becomes 1.
	_, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}

	if err := store.MarkFailed(tx.ID, "transient error"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	got, err := store.GetByID(tx.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	// attempts=1 < MaxAttempts=3 → should be requeued to pending.
	if got.Status != models.StatusPending {
		t.Errorf("Status: got %q, want %q (should be requeued)", got.Status, models.StatusPending)
	}
	if got.RetryAfter == nil {
		t.Error("RetryAfter should be set after first failure (exponential backoff)")
	}
	if got.Error != "transient error" {
		t.Errorf("Error: got %q, want %q", got.Error, "transient error")
	}
}

// TestRetryFullCycle simulates the complete retry path: a transcript fails
// MaxAttempts times and ends up permanently failed.
func TestRetryFullCycle(t *testing.T) {
	tx := newTranscript("Fail me until permanent.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for attempt := 1; attempt <= db.MaxAttempts; attempt++ {
		// Bypass the retry_after wait by resetting it directly.
		if _, err := conn.Exec(`UPDATE transcripts SET retry_after = NULL WHERE id = $1`, tx.ID); err != nil {
			t.Fatalf("reset retry_after: %v", err)
		}

		claimed, err := store.ClaimPending()
		if err != nil {
			t.Fatalf("ClaimPending (attempt %d): %v", attempt, err)
		}
		if claimed == nil {
			t.Fatalf("ClaimPending returned nil on attempt %d (retry_after not cleared?)", attempt)
		}

		if err := store.MarkFailed(tx.ID, fmt.Sprintf("error on attempt %d", attempt)); err != nil {
			t.Fatalf("MarkFailed (attempt %d): %v", attempt, err)
		}

		got, err := store.GetByID(tx.ID)
		if err != nil {
			t.Fatalf("GetByID (attempt %d): %v", attempt, err)
		}

		if attempt < db.MaxAttempts {
			if got.Status != models.StatusPending {
				t.Errorf("attempt %d: status = %q, want pending (should requeue)", attempt, got.Status)
			}
			if got.RetryAfter == nil {
				t.Errorf("attempt %d: retry_after should be set for backoff", attempt)
			}
		} else {
			// Final attempt — should be permanently failed.
			if got.Status != models.StatusFailed {
				t.Errorf("final attempt: status = %q, want failed", got.Status)
			}
			if got.RetryAfter != nil {
				t.Errorf("final attempt: retry_after should be nil for permanent failure, got %v", got.RetryAfter)
			}
		}
	}

	// Verify permanently failed transcript is NOT re-claimable.
	if _, err := conn.Exec(`UPDATE transcripts SET retry_after = NULL WHERE id = $1`, tx.ID); err != nil {
		t.Fatalf("reset retry_after: %v", err)
	}
	claimed, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("ClaimPending after permanent failure: %v", err)
	}
	if claimed != nil && claimed.ID == tx.ID {
		t.Error("permanently failed transcript should not be re-claimable")
	}
}

// TestBackoffIncreases verifies that retry_after grows with each failed attempt.
func TestBackoffIncreases(t *testing.T) {
	tx := newTranscript("Backoff test.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var retryAfters []*time.Time

	for attempt := 1; attempt < db.MaxAttempts; attempt++ {
		if _, err := conn.Exec(`UPDATE transcripts SET retry_after = NULL WHERE id = $1`, tx.ID); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := store.ClaimPending(); err != nil {
			t.Fatalf("ClaimPending: %v", err)
		}
		if err := store.MarkFailed(tx.ID, "backoff test"); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}
		got, err := store.GetByID(tx.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		retryAfters = append(retryAfters, got.RetryAfter)
	}

	// Each retry_after should be further in the future than the previous.
	for i := 1; i < len(retryAfters); i++ {
		if retryAfters[i] == nil || retryAfters[i-1] == nil {
			t.Fatalf("retry_after nil on attempt %d", i)
		}
		if !retryAfters[i].After(*retryAfters[i-1]) {
			t.Errorf("attempt %d retry_after (%v) not after attempt %d (%v) — backoff not increasing",
				i+1, retryAfters[i], i, retryAfters[i-1])
		}
	}
}

// TestClaimPendingRespectsRetryAfter verifies that a transcript with a future
// retry_after is NOT claimable until the time has passed.
func TestClaimPendingRespectsRetryAfter(t *testing.T) {
	tx := newTranscript("Backoff wait test.")
	cleanup(t, tx.ID)

	if err := store.Create(tx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Manually set retry_after far in the future.
	if _, err := conn.Exec(
		`UPDATE transcripts SET status = 'pending', retry_after = NOW() + INTERVAL '1 hour' WHERE id = $1`,
		tx.ID,
	); err != nil {
		t.Fatalf("set future retry_after: %v", err)
	}

	claimed, err := store.ClaimPending()
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if claimed != nil && claimed.ID == tx.ID {
		t.Error("transcript with future retry_after should not be claimable")
	}
}
