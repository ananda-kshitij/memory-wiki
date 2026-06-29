package memory

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Codex-AK/memory-wiki/internal/models"
)

// ---------------------------------------------------------------------------
// mockObjectClient — in-memory implementation of ObjectClient
// ---------------------------------------------------------------------------

type mockObjectClient struct {
	mu      sync.Mutex
	data    map[string][]byte
	putErr  error
	getErr  error
	listErr error
}

func newMock() *mockObjectClient {
	return &mockObjectClient{data: make(map[string][]byte)}
}

func (m *mockObjectClient) Put(_ context.Context, key string, data []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.data[key] = cp
	return nil
}

func (m *mockObjectClient) Get(_ context.Context, key string) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.data[key]
	if !ok {
		return nil, errors.New("NoSuchKey: object not found")
	}
	cp := make([]byte, len(d))
	copy(cp, d)
	return cp, nil
}

func (m *mockObjectClient) List(_ context.Context, prefix string) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockObjectClient) GrepAll(_ context.Context, term string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lower := strings.ToLower(term)
	var matches []string
	for k, v := range m.data {
		if strings.Contains(strings.ToLower(string(v)), lower) {
			matches = append(matches, k)
		}
	}
	return matches, nil
}

// ---------------------------------------------------------------------------
// mockReconciler — returns existing body + new content (simulates merge)
// ---------------------------------------------------------------------------

type mockReconciler struct{ err error }

func (r *mockReconciler) ReconcileMemory(_ context.Context, existing string, entry models.MemoryEntry, _ string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	_, lines := parse([]byte(existing))
	merged := append(lines, "", "---", "", entry.Content)
	return strings.Join(merged, "\n"), nil
}

// ---------------------------------------------------------------------------
// mockEmbedder + mockEmbeddingStorer for semantic search tests
// ---------------------------------------------------------------------------

type mockEmbedder struct {
	vec []float32
	err error
}

func (e *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return e.vec, e.err
}

type mockEmbeddingStorer struct {
	mu           sync.Mutex
	stored       map[string][]float32
	upsertErr    error
	searchResult []string
	searchErr    error
	searchCalled bool
}

func newMockEmbeddingStorer() *mockEmbeddingStorer {
	return &mockEmbeddingStorer{stored: make(map[string][]float32)}
}

func (s *mockEmbeddingStorer) Upsert(_ context.Context, path string, vec []float32) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored[path] = vec
	return nil
}

func (s *mockEmbeddingStorer) Search(_ context.Context, _ []float32, _ int) ([]string, error) {
	s.searchCalled = true
	return s.searchResult, s.searchErr
}

// ---------------------------------------------------------------------------
// mergeTags tests
// ---------------------------------------------------------------------------

func TestMergeTagsEmptyInputs(t *testing.T) {
	got := mergeTags(nil, nil)
	if len(got) != 0 {
		t.Errorf("want empty slice, got %v", got)
	}
}

func TestMergeTagsNoDuplicates(t *testing.T) {
	got := mergeTags([]string{"a", "b"}, []string{"c", "d"})
	want := []string{"a", "b", "c", "d"}
	if !slicesEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeTagsDeduplicates(t *testing.T) {
	got := mergeTags([]string{"go", "api"}, []string{"api", "testing", "go"})
	if len(got) != 3 {
		t.Errorf("expected 3 unique tags, got %d: %v", len(got), got)
	}
	if !containsStr(got, "go") || !containsStr(got, "api") || !containsStr(got, "testing") {
		t.Errorf("missing expected tags in %v", got)
	}
}

func TestMergeTagsPreservesOrder(t *testing.T) {
	got := mergeTags([]string{"z", "a"}, []string{"m", "a"})
	if len(got) != 3 {
		t.Fatalf("expected 3 tags, got %d: %v", len(got), got)
	}
	if got[0] != "z" || got[1] != "a" || got[2] != "m" {
		t.Errorf("unexpected order: %v", got)
	}
}

func TestMergeTagsDuplicatesInIncoming(t *testing.T) {
	got := mergeTags(nil, []string{"x", "x", "y", "x"})
	if len(got) != 2 {
		t.Errorf("expected 2 unique tags, got %d: %v", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// parse / render round-trip tests
// ---------------------------------------------------------------------------

func TestParseRenderRoundTrip(t *testing.T) {
	fm := frontmatter{
		Tags:        []string{"go", "testing"},
		LastUpdated: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		SourceIDs:   []string{"tid-001", "tid-002"},
	}
	body := "## Notes\n\nSome content here."

	data, err := render(fm, body)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	gotFM, gotLines := parse(data)

	if !slicesEqual(gotFM.Tags, fm.Tags) {
		t.Errorf("tags: got %v, want %v", gotFM.Tags, fm.Tags)
	}
	if !slicesEqual(gotFM.SourceIDs, fm.SourceIDs) {
		t.Errorf("source IDs: got %v, want %v", gotFM.SourceIDs, fm.SourceIDs)
	}
	if !gotFM.LastUpdated.Equal(fm.LastUpdated) {
		t.Errorf("last_updated: got %v, want %v", gotFM.LastUpdated, fm.LastUpdated)
	}

	gotBody := strings.Join(gotLines, "\n")
	if !strings.Contains(gotBody, "Some content here.") {
		t.Errorf("body not preserved; got: %q", gotBody)
	}
}

func TestParseNonFrontmatter(t *testing.T) {
	data := []byte("plain text without frontmatter")
	fm, lines := parse(data)

	if len(fm.Tags) != 0 || len(fm.SourceIDs) != 0 {
		t.Errorf("expected empty frontmatter, got %+v", fm)
	}
	if len(lines) == 0 {
		t.Error("expected non-empty body lines")
	}
	_ = lines
}

func TestRenderStartsWithFrontmatterDelimiter(t *testing.T) {
	fm := frontmatter{Tags: []string{"t1"}}
	data, err := render(fm, "body")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("---\n")) {
		t.Errorf("rendered output does not start with ---\\n: %q", string(data[:min(len(data), 20)]))
	}
}

// ---------------------------------------------------------------------------
// Upsert tests
// ---------------------------------------------------------------------------

func TestUpsertCreatesNewFile(t *testing.T) {
	mock := newMock()
	store := NewStore(mock, &mockReconciler{})
	ctx := context.Background()

	entry := models.MemoryEntry{
		Category: "people",
		Name:     "alice",
		Tags:     []string{"engineer", "go"},
		Content:  "Alice is a software engineer who loves Go.",
	}

	if err := store.Upsert(ctx, entry, "tx-001"); err != nil {
		t.Fatalf("Upsert error: %v", err)
	}

	key := "memories/people/alice.md"
	data, ok := mock.data[key]
	if !ok {
		t.Fatalf("expected object at key %q to exist", key)
	}

	text := string(data)
	if !strings.Contains(text, "engineer") {
		t.Errorf("expected tag 'engineer' in output; got:\n%s", text)
	}
	if !strings.Contains(text, "Alice is a software engineer") {
		t.Errorf("expected content in output; got:\n%s", text)
	}
	if !strings.Contains(text, "tx-001") {
		t.Errorf("expected source_transcript_ids to contain tx-001; got:\n%s", text)
	}
}

func TestUpsertMergesExistingFile(t *testing.T) {
	mock := newMock()
	store := NewStore(mock, &mockReconciler{})
	ctx := context.Background()

	entry1 := models.MemoryEntry{
		Category: "topics",
		Name:     "machine-learning",
		Tags:     []string{"ml", "ai"},
		Content:  "First note about ML.",
	}
	entry2 := models.MemoryEntry{
		Category: "topics",
		Name:     "machine-learning",
		Tags:     []string{"ai", "deep-learning"},
		Content:  "Second note about deep learning.",
	}

	if err := store.Upsert(ctx, entry1, "tx-001"); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := store.Upsert(ctx, entry2, "tx-002"); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	key := "memories/topics/machine-learning.md"
	data := mock.data[key]
	text := string(data)

	if !strings.Contains(text, "First note") {
		t.Error("expected first note content to be preserved")
	}
	if !strings.Contains(text, "Second note") {
		t.Error("expected second note content to be present")
	}

	fm, _ := parse(data)
	if len(fm.Tags) != 3 {
		t.Errorf("expected 3 unique tags after merge, got %d: %v", len(fm.Tags), fm.Tags)
	}
	if !slicesEqual(fm.SourceIDs, []string{"tx-001", "tx-002"}) {
		t.Errorf("source IDs: got %v, want [tx-001 tx-002]", fm.SourceIDs)
	}
}

func TestUpsertDeduplicatesTranscriptID(t *testing.T) {
	mock := newMock()
	store := NewStore(mock, &mockReconciler{})
	ctx := context.Background()

	entry := models.MemoryEntry{
		Category: "people",
		Name:     "bob",
		Tags:     []string{"dev"},
		Content:  "Bob is a developer.",
	}

	_ = store.Upsert(ctx, entry, "tx-dup")
	_ = store.Upsert(ctx, entry, "tx-dup")

	data := mock.data["memories/people/bob.md"]
	fm, _ := parse(data)

	count := 0
	for _, id := range fm.SourceIDs {
		if id == "tx-dup" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("transcript ID tx-dup appears %d times, want 1", count)
	}
}

func TestUpsertPutError(t *testing.T) {
	mock := newMock()
	mock.putErr = errors.New("storage unavailable")
	store := NewStore(mock, &mockReconciler{})

	entry := models.MemoryEntry{
		Category: "projects",
		Name:     "alpha",
		Tags:     []string{},
		Content:  "Project alpha notes.",
	}

	err := store.Upsert(context.Background(), entry, "tx-err")
	if err == nil {
		t.Fatal("expected error from Put, got nil")
	}
}

// TestUpsertReconciliationFallback verifies that when ReconcileMemory fails the
// store falls back to appending the new content rather than returning an error.
func TestUpsertReconciliationFallback(t *testing.T) {
	mock := newMock()
	ctx := context.Background()

	first := models.MemoryEntry{
		Category: "people",
		Name:     "carol",
		Tags:     []string{"engineer"},
		Content:  "Carol is an engineer.",
	}
	if err := NewStore(mock, &mockReconciler{}).Upsert(ctx, first, "tx-001"); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	// Second upsert uses a reconciler that always fails — should fall back to append.
	second := models.MemoryEntry{
		Category: "people",
		Name:     "carol",
		Tags:     []string{"manager"},
		Content:  "Carol is now a manager.",
	}
	if err := NewStore(mock, &mockReconciler{err: errors.New("LLM unavailable")}).Upsert(ctx, second, "tx-002"); err != nil {
		t.Fatalf("Upsert with broken reconciler should not error: %v", err)
	}

	text := string(mock.data["memories/people/carol.md"])
	if !strings.Contains(text, "Carol is an engineer.") {
		t.Error("expected original content to be preserved on fallback")
	}
	if !strings.Contains(text, "Carol is now a manager.") {
		t.Error("expected new content to be appended on fallback")
	}
}

// ---------------------------------------------------------------------------
// Embedding tests
// ---------------------------------------------------------------------------

func TestUpsertStoresEmbedding(t *testing.T) {
	mock := newMock()
	idx := newMockEmbeddingStorer()
	emb := &mockEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	store := NewStore(mock, &mockReconciler{}).WithEmbeddings(emb, idx)

	entry := models.MemoryEntry{
		Category: "topics",
		Name:     "golang",
		Tags:     []string{"go"},
		Content:  "Go is a statically typed language.",
	}
	if err := store.Upsert(context.Background(), entry, "tx-emb"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if _, ok := idx.stored["memories/topics/golang.md"]; !ok {
		t.Error("expected embedding to be stored for memories/topics/golang.md")
	}
}

func TestUpsertEmbeddingErrorDoesNotFail(t *testing.T) {
	mock := newMock()
	idx := newMockEmbeddingStorer()
	emb := &mockEmbedder{err: errors.New("embed API down")}
	store := NewStore(mock, &mockReconciler{}).WithEmbeddings(emb, idx)

	entry := models.MemoryEntry{
		Category: "topics",
		Name:     "rust",
		Tags:     []string{},
		Content:  "Rust is a systems language.",
	}
	if err := store.Upsert(context.Background(), entry, "tx-001"); err != nil {
		t.Fatalf("Upsert should succeed even when embedding fails: %v", err)
	}
	if _, ok := mock.data["memories/topics/rust.md"]; !ok {
		t.Error("expected memory file to be stored even when embedding fails")
	}
}

func TestGrepSemanticModeUsesEmbedder(t *testing.T) {
	mock := newMock()
	idx := newMockEmbeddingStorer()
	idx.searchResult = []string{"memories/people/alice.md"}
	emb := &mockEmbedder{vec: []float32{0.1, 0.2}}
	store := NewStore(mock, &mockReconciler{}).WithEmbeddings(emb, idx)

	matches, err := store.Grep(context.Background(), "engineer", true)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !idx.searchCalled {
		t.Error("expected EmbeddingStorer.Search to be called in semantic mode")
	}
	if len(matches) != 1 || matches[0] != "memories/people/alice.md" {
		t.Errorf("unexpected matches: %v", matches)
	}
}

func TestGrepKeywordModeSkipsEmbedder(t *testing.T) {
	mock := newMock()
	mock.data["memories/people/dave.md"] = []byte("Dave loves Rust")
	idx := newMockEmbeddingStorer()
	emb := &mockEmbedder{vec: []float32{0.1}}
	store := NewStore(mock, &mockReconciler{}).WithEmbeddings(emb, idx)

	_, err := store.Grep(context.Background(), "rust", false)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if idx.searchCalled {
		t.Error("EmbeddingStorer.Search should NOT be called in keyword mode")
	}
}

func TestGrepSemanticFallsBackOnEmbedError(t *testing.T) {
	mock := newMock()
	mock.data["memories/people/eve.md"] = []byte("Eve loves Go")
	idx := newMockEmbeddingStorer()
	emb := &mockEmbedder{err: errors.New("embed down")}
	store := NewStore(mock, &mockReconciler{}).WithEmbeddings(emb, idx)

	matches, err := store.Grep(context.Background(), "go", true)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(matches) == 0 {
		t.Error("expected keyword fallback to return matches")
	}
}

// ---------------------------------------------------------------------------
// List / Cat / Grep delegation tests
// ---------------------------------------------------------------------------

func TestListAddsMemoriesPrefix(t *testing.T) {
	mock := newMock()
	mock.data["memories/people/alice.md"] = []byte("alice")
	mock.data["memories/topics/go.md"] = []byte("go")
	mock.data["other/file.md"] = []byte("other")

	store := NewStore(mock, &mockReconciler{})
	keys, err := store.List(context.Background(), "people/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "memories/people/") {
			t.Errorf("unexpected key outside prefix: %q", k)
		}
	}
}

func TestCatAddsMemoriesPrefix(t *testing.T) {
	mock := newMock()
	mock.data["memories/people/carol.md"] = []byte("carol content")

	store := NewStore(mock, &mockReconciler{})
	data, err := store.Cat(context.Background(), "people/carol.md")
	if err != nil {
		t.Fatalf("Cat: %v", err)
	}
	if string(data) != "carol content" {
		t.Errorf("got %q, want %q", data, "carol content")
	}
}

func TestGrepDelegates(t *testing.T) {
	mock := newMock()
	mock.data["memories/people/dave.md"] = []byte("Dave loves Rust")
	mock.data["memories/people/eve.md"] = []byte("Eve loves Go")

	store := NewStore(mock, &mockReconciler{})
	matches, err := store.Grep(context.Background(), "rust", false)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(matches) != 1 || matches[0] != "memories/people/dave.md" {
		t.Errorf("expected only dave.md, got %v", matches)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
