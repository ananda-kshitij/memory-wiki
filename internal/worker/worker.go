package worker

import (
	"context"
	"log"
	"time"

	"github.com/Codex-AK/memory-wiki/internal/models"
	"github.com/Codex-AK/memory-wiki/internal/storage/db"
)

// TranscriptClaimer handles claiming and updating transcript status in the DB.
type TranscriptClaimer interface {
	ClaimPending() (*models.Transcript, error)
	MarkDone(id string) error
	MarkFailed(id string, reason string) error
}

// LLMExtractor extracts memory entries from a conversation transcript.
type LLMExtractor interface {
	ExtractMemories(ctx context.Context, transcript string) ([]models.MemoryEntry, error)
}

// MemoryUpserter persists extracted memory entries to object storage.
type MemoryUpserter interface {
	Upsert(ctx context.Context, entry models.MemoryEntry, transcriptID string) error
}

type Worker struct {
	store    TranscriptClaimer
	llm      LLMExtractor
	memories MemoryUpserter
	interval time.Duration
}

func New(store TranscriptClaimer, llmClient LLMExtractor, memStore MemoryUpserter) *Worker {
	return &Worker{
		store:    store,
		llm:      llmClient,
		memories: memStore,
		interval: 5 * time.Second,
	}
}

// SetInterval overrides the polling interval. Useful for tests.
func (w *Worker) SetInterval(d time.Duration) *Worker {
	w.interval = d
	return w
}

// Run polls for pending transcripts until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	log.Println("worker: starting")
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("worker: stopping")
			return
		case <-ticker.C:
			w.processOne(ctx)
		}
	}
}

func (w *Worker) processOne(ctx context.Context) {
	t, err := w.store.ClaimPending()
	if err != nil {
		log.Printf("worker: claim error: %v", err)
		return
	}
	if t == nil {
		return // nothing to do
	}

	log.Printf("worker: processing transcript %s (attempt %d/%d)", t.ID, t.Attempts, db.MaxAttempts)

	entries, err := w.llm.ExtractMemories(ctx, t.Content)
	if err != nil {
		log.Printf("worker: llm error for %s: %v", t.ID, err)
		w.failTranscript(t.ID, t.Attempts, err.Error())
		return
	}

	for _, entry := range entries {
		if err := w.memories.Upsert(ctx, entry, t.ID); err != nil {
			log.Printf("worker: upsert error for %s/%s: %v", entry.Category, entry.Name, err)
			w.failTranscript(t.ID, t.Attempts, err.Error())
			return
		}
	}

	if err := w.store.MarkDone(t.ID); err != nil {
		log.Printf("worker: mark done error for %s: %v", t.ID, err)
	} else {
		log.Printf("worker: done %s (%d memories extracted)", t.ID, len(entries))
	}
}

// failTranscript calls MarkFailed and logs whether the transcript will be
// retried or permanently failed based on the current attempt count.
func (w *Worker) failTranscript(id string, attempts int, reason string) {
	if attempts >= db.MaxAttempts {
		log.Printf("worker: permanently failed %s after %d attempts: %s", id, attempts, reason)
	} else {
		log.Printf("worker: requeued %s for retry (attempt %d/%d): %s", id, attempts, db.MaxAttempts, reason)
	}
	if err := w.store.MarkFailed(id, reason); err != nil {
		log.Printf("worker: mark failed error for %s: %v", id, err)
	}
}
