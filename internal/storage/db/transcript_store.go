package db

import (
	"database/sql"
	"fmt"

	"github.com/Codex-AK/memory-wiki/internal/models"
)

// MaxAttempts is the number of times a transcript will be retried before being
// permanently marked as failed.
const MaxAttempts = 3

// BackoffBaseSeconds is the base interval for exponential backoff between retries.
// Delays: attempt 1 → 5s, attempt 2 → 10s, attempt 3 → permanent failure.
const BackoffBaseSeconds = 5

type TranscriptStore struct {
	db *sql.DB
}

func NewTranscriptStore(db *sql.DB) *TranscriptStore {
	return &TranscriptStore{db: db}
}

func (s *TranscriptStore) Create(t *models.Transcript) error {
	_, err := s.db.Exec(
		`INSERT INTO transcripts (id, content, status, error, attempts, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID, t.Content, t.Status, t.Error, t.Attempts, t.CreatedAt, t.UpdatedAt,
	)
	return err
}

func (s *TranscriptStore) GetByID(id string) (*models.Transcript, error) {
	t := &models.Transcript{}
	err := s.db.QueryRow(
		`SELECT id, content, status, error, attempts, retry_after, created_at, updated_at
		 FROM transcripts WHERE id = $1`, id,
	).Scan(&t.ID, &t.Content, &t.Status, &t.Error, &t.Attempts, &t.RetryAfter, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// ClaimPending atomically claims one pending transcript for processing,
// incrementing its attempt counter. Only transcripts with attempts < MaxAttempts
// and whose retry_after time has passed are eligible.
// Returns nil, nil if no pending transcripts are available.
func (s *TranscriptStore) ClaimPending() (*models.Transcript, error) {
	t := &models.Transcript{}
	err := s.db.QueryRow(`
		UPDATE transcripts
		SET status = 'processing', attempts = attempts + 1, updated_at = NOW()
		WHERE id = (
			SELECT id FROM transcripts
			WHERE status = 'pending'
			  AND attempts < $1
			  AND (retry_after IS NULL OR retry_after <= NOW())
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, content, status, error, attempts, retry_after, created_at, updated_at`,
		MaxAttempts,
	).Scan(&t.ID, &t.Content, &t.Status, &t.Error, &t.Attempts, &t.RetryAfter, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *TranscriptStore) MarkDone(id string) error {
	_, err := s.db.Exec(
		`UPDATE transcripts SET status = 'done', error = '', retry_after = NULL, updated_at = NOW() WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("update transcript %s: %w", id, err)
	}
	return nil
}

// MarkFailed records an error on the transcript. If the transcript has reached
// MaxAttempts it is permanently set to 'failed'; otherwise it is reset to
// 'pending' with an exponential backoff delay: base * 2^(attempts-1) seconds.
func (s *TranscriptStore) MarkFailed(id string, reason string) error {
	_, err := s.db.Exec(
		`UPDATE transcripts
		 SET status      = CASE WHEN attempts >= $2 THEN 'failed' ELSE 'pending' END,
		     error       = $3,
		     retry_after = CASE
		                       WHEN attempts < $2
		                       THEN NOW() + (POWER(2, attempts - 1) * $4 * INTERVAL '1 second')
		                       ELSE NULL
		                   END,
		     updated_at  = NOW()
		 WHERE id = $1`,
		id, MaxAttempts, reason, BackoffBaseSeconds,
	)
	if err != nil {
		return fmt.Errorf("update transcript %s: %w", id, err)
	}
	return nil
}
