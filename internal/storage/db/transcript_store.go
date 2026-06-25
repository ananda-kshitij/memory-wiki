package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/Codex-AK/memory-wiki/internal/models"
)

type TranscriptStore struct {
	db *sql.DB
}

func NewTranscriptStore(db *sql.DB) *TranscriptStore {
	return &TranscriptStore{db: db}
}

func (s *TranscriptStore) Create(t *models.Transcript) error {
	_, err := s.db.Exec(
		`INSERT INTO transcripts (id, content, status, error, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		t.ID, t.Content, t.Status, t.Error, t.CreatedAt, t.UpdatedAt,
	)
	return err
}

func (s *TranscriptStore) GetByID(id string) (*models.Transcript, error) {
	t := &models.Transcript{}
	err := s.db.QueryRow(
		`SELECT id, content, status, error, created_at, updated_at
		 FROM transcripts WHERE id = $1`, id,
	).Scan(&t.ID, &t.Content, &t.Status, &t.Error, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// ClaimPending atomically claims one pending transcript for processing.
// Returns nil, nil if no pending transcripts exist.
func (s *TranscriptStore) ClaimPending() (*models.Transcript, error) {
	t := &models.Transcript{}
	err := s.db.QueryRow(`
		UPDATE transcripts
		SET status = 'processing', updated_at = NOW()
		WHERE id = (
			SELECT id FROM transcripts
			WHERE status = 'pending'
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, content, status, error, created_at, updated_at`,
	).Scan(&t.ID, &t.Content, &t.Status, &t.Error, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *TranscriptStore) MarkDone(id string) error {
	return s.setStatus(id, models.StatusDone, "")
}

func (s *TranscriptStore) MarkFailed(id string, reason string) error {
	return s.setStatus(id, models.StatusFailed, reason)
}

func (s *TranscriptStore) setStatus(id string, status models.TranscriptStatus, errMsg string) error {
	_, err := s.db.Exec(
		`UPDATE transcripts SET status = $1, error = $2, updated_at = $3 WHERE id = $4`,
		status, errMsg, time.Now(), id,
	)
	if err != nil {
		return fmt.Errorf("update transcript %s: %w", id, err)
	}
	return nil
}
