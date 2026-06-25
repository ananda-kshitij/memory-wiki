package models

import "time"

type TranscriptStatus string

const (
	StatusPending    TranscriptStatus = "pending"
	StatusProcessing TranscriptStatus = "processing"
	StatusDone       TranscriptStatus = "done"
	StatusFailed     TranscriptStatus = "failed"
)

type Transcript struct {
	ID        string           `json:"id"`
	Content   string           `json:"content"`
	Status    TranscriptStatus `json:"status"`
	Error     string           `json:"error,omitempty"`
	Attempts  int              `json:"attempts"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}
