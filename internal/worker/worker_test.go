package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/Codex-AK/memory-wiki/internal/models"
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

type stubClaimer struct {
	transcript      *models.Transcript
	claimErr        error
	markDoneErr     error
	markFailedErr   error
	markDoneCalled  bool
	markFailedCalls []string // collects reasons
}

func (s *stubClaimer) ClaimPending() (*models.Transcript, error) {
	return s.transcript, s.claimErr
}

func (s *stubClaimer) MarkDone(_ string) error {
	s.markDoneCalled = true
	return s.markDoneErr
}

func (s *stubClaimer) MarkFailed(_ string, reason string) error {
	s.markFailedCalls = append(s.markFailedCalls, reason)
	return s.markFailedErr
}

type stubExtractor struct {
	entries []models.MemoryEntry
	err     error
}

func (s *stubExtractor) ExtractMemories(_ context.Context, _ string) ([]models.MemoryEntry, error) {
	return s.entries, s.err
}

type stubUpserter struct {
	err         error
	callCount   int
	lastEntry   models.MemoryEntry
	lastTxID    string
}

func (s *stubUpserter) Upsert(_ context.Context, entry models.MemoryEntry, txID string) error {
	s.callCount++
	s.lastEntry = entry
	s.lastTxID = txID
	return s.err
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestProcessOneNoTranscript(t *testing.T) {
	claimer := &stubClaimer{transcript: nil}
	extractor := &stubExtractor{}
	upserter := &stubUpserter{}

	w := New(claimer, extractor, upserter)
	w.processOne(context.Background())

	if claimer.markDoneCalled {
		t.Error("MarkDone should not be called when there is nothing to process")
	}
	if len(claimer.markFailedCalls) > 0 {
		t.Error("MarkFailed should not be called when there is nothing to process")
	}
	if extractor.entries != nil || upserter.callCount > 0 {
		t.Error("LLM and upsert should not be invoked when queue is empty")
	}
}

func TestProcessOneClaimError(t *testing.T) {
	claimer := &stubClaimer{
		transcript: nil,
		claimErr:   errors.New("db connection lost"),
	}
	extractor := &stubExtractor{}
	upserter := &stubUpserter{}

	w := New(claimer, extractor, upserter)
	w.processOne(context.Background()) // should not panic

	if claimer.markDoneCalled || len(claimer.markFailedCalls) > 0 {
		t.Error("no status update should happen on claim error")
	}
}

func TestProcessOneHappyPath(t *testing.T) {
	tx := &models.Transcript{ID: "tx-happy", Content: "Alice works at Acme."}
	entries := []models.MemoryEntry{
		{Category: "people", Name: "alice", Tags: []string{"acme"}, Content: "Alice works at Acme."},
	}

	claimer := &stubClaimer{transcript: tx}
	extractor := &stubExtractor{entries: entries}
	upserter := &stubUpserter{}

	w := New(claimer, extractor, upserter)
	w.processOne(context.Background())

	if upserter.callCount != 1 {
		t.Errorf("expected 1 Upsert call, got %d", upserter.callCount)
	}
	if upserter.lastTxID != "tx-happy" {
		t.Errorf("Upsert called with wrong transcript ID: %q", upserter.lastTxID)
	}
	if !claimer.markDoneCalled {
		t.Error("expected MarkDone to be called")
	}
	if len(claimer.markFailedCalls) > 0 {
		t.Errorf("expected no MarkFailed calls, got %v", claimer.markFailedCalls)
	}
}

func TestProcessOneHappyPathMultipleEntries(t *testing.T) {
	tx := &models.Transcript{ID: "tx-multi", Content: "Alice and Bob are coworkers."}
	entries := []models.MemoryEntry{
		{Category: "people", Name: "alice", Tags: []string{}, Content: "Alice is an engineer."},
		{Category: "people", Name: "bob", Tags: []string{}, Content: "Bob is a designer."},
	}

	claimer := &stubClaimer{transcript: tx}
	extractor := &stubExtractor{entries: entries}
	upserter := &stubUpserter{}

	w := New(claimer, extractor, upserter)
	w.processOne(context.Background())

	if upserter.callCount != 2 {
		t.Errorf("expected 2 Upsert calls for 2 entries, got %d", upserter.callCount)
	}
	if !claimer.markDoneCalled {
		t.Error("expected MarkDone to be called")
	}
}

func TestProcessOneLLMError(t *testing.T) {
	tx := &models.Transcript{ID: "tx-llm-err", Content: "some content"}
	claimer := &stubClaimer{transcript: tx}
	extractor := &stubExtractor{err: errors.New("API rate limit exceeded")}
	upserter := &stubUpserter{}

	w := New(claimer, extractor, upserter)
	w.processOne(context.Background())

	if upserter.callCount > 0 {
		t.Error("Upsert should not be called when LLM fails")
	}
	if claimer.markDoneCalled {
		t.Error("MarkDone should not be called when LLM fails")
	}
	if len(claimer.markFailedCalls) != 1 {
		t.Errorf("expected 1 MarkFailed call, got %d", len(claimer.markFailedCalls))
	}
	if claimer.markFailedCalls[0] != "API rate limit exceeded" {
		t.Errorf("unexpected MarkFailed reason: %q", claimer.markFailedCalls[0])
	}
}

func TestProcessOneUpsertError(t *testing.T) {
	tx := &models.Transcript{ID: "tx-upsert-err", Content: "some content"}
	entries := []models.MemoryEntry{
		{Category: "topics", Name: "go", Tags: []string{}, Content: "Go programming."},
	}

	claimer := &stubClaimer{transcript: tx}
	extractor := &stubExtractor{entries: entries}
	upserter := &stubUpserter{err: errors.New("storage full")}

	w := New(claimer, extractor, upserter)
	w.processOne(context.Background())

	if claimer.markDoneCalled {
		t.Error("MarkDone should not be called when Upsert fails")
	}
	if len(claimer.markFailedCalls) != 1 {
		t.Errorf("expected 1 MarkFailed call, got %d", len(claimer.markFailedCalls))
	}
}

func TestProcessOneMarkDoneError(t *testing.T) {
	// MarkDone error is logged but should not affect overall flow (no panic/crash).
	tx := &models.Transcript{ID: "tx-done-err", Content: "some content"}
	claimer := &stubClaimer{
		transcript:  tx,
		markDoneErr: errors.New("lost connection on final update"),
	}
	extractor := &stubExtractor{entries: []models.MemoryEntry{}}
	upserter := &stubUpserter{}

	w := New(claimer, extractor, upserter)
	// Should complete without panic even when MarkDone fails.
	w.processOne(context.Background())

	if !claimer.markDoneCalled {
		t.Error("MarkDone was not attempted")
	}
}

func TestProcessOneEmptyLLMResponse(t *testing.T) {
	// LLM returns no entries — still should mark done.
	tx := &models.Transcript{ID: "tx-empty", Content: "nothing extractable"}
	claimer := &stubClaimer{transcript: tx}
	extractor := &stubExtractor{entries: []models.MemoryEntry{}}
	upserter := &stubUpserter{}

	w := New(claimer, extractor, upserter)
	w.processOne(context.Background())

	if upserter.callCount != 0 {
		t.Errorf("expected 0 Upsert calls for empty entries, got %d", upserter.callCount)
	}
	if !claimer.markDoneCalled {
		t.Error("expected MarkDone to be called even with 0 entries")
	}
}
