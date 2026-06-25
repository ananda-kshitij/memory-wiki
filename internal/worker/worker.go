package worker

import (
	"context"
	"log"
	"time"

	"github.com/Codex-AK/memory-wiki/internal/llm"
	"github.com/Codex-AK/memory-wiki/internal/memory"
	"github.com/Codex-AK/memory-wiki/internal/storage/db"
)

type Worker struct {
	store    *db.TranscriptStore
	llm      *llm.Client
	memories *memory.Store
	interval time.Duration
}

func New(store *db.TranscriptStore, llmClient *llm.Client, memStore *memory.Store) *Worker {
	return &Worker{
		store:    store,
		llm:      llmClient,
		memories: memStore,
		interval: 5 * time.Second,
	}
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

	log.Printf("worker: processing transcript %s", t.ID)

	entries, err := w.llm.ExtractMemories(ctx, t.Content)
	if err != nil {
		log.Printf("worker: llm error for %s: %v", t.ID, err)
		_ = w.store.MarkFailed(t.ID, err.Error())
		return
	}

	for _, entry := range entries {
		if err := w.memories.Upsert(ctx, entry, t.ID); err != nil {
			log.Printf("worker: upsert error for %s/%s: %v", entry.Category, entry.Name, err)
			_ = w.store.MarkFailed(t.ID, err.Error())
			return
		}
	}

	if err := w.store.MarkDone(t.ID); err != nil {
		log.Printf("worker: mark done error for %s: %v", t.ID, err)
	} else {
		log.Printf("worker: done %s (%d memories extracted)", t.ID, len(entries))
	}
}
