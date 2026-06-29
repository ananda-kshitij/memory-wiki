package memory

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Codex-AK/memory-wiki/internal/models"
	"go.yaml.in/yaml/v3"
)

// ObjectClient is the interface for object storage operations used by Store.
type ObjectClient interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]string, error)
	GrepAll(ctx context.Context, term string) ([]string, error)
}

// Reconciler is the interface the Store uses to merge memory content.
type Reconciler interface {
	ReconcileMemory(ctx context.Context, existingContent string, entry models.MemoryEntry, transcriptID string) (string, error)
}

// Embedder converts text to a vector embedding for semantic search.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// EmbeddingStorer persists and queries vector embeddings.
type EmbeddingStorer interface {
	Upsert(ctx context.Context, path string, embedding []float32) error
	Search(ctx context.Context, query []float32, limit int) ([]string, error)
}

type Store struct {
	obj ObjectClient
	llm Reconciler
	emb Embedder        // nil → semantic search unavailable
	idx EmbeddingStorer // nil → semantic search unavailable
}

func NewStore(obj ObjectClient, llm Reconciler) *Store {
	return &Store{obj: obj, llm: llm}
}

// WithEmbeddings attaches an embedder and embedding store for semantic search.
func (s *Store) WithEmbeddings(emb Embedder, idx EmbeddingStorer) *Store {
	s.emb = emb
	s.idx = idx
	return s
}

type frontmatter struct {
	Tags        []string  `yaml:"tags"`
	LastUpdated time.Time `yaml:"last_updated"`
	SourceIDs   []string  `yaml:"source_transcript_ids"`
}

// Upsert merges a new memory entry into the existing file (or creates it).
// When the file already exists, it calls the LLM to reconcile the existing
// content with the new entry into a single coherent, deduplicated body.
// If an embedder is configured, it also stores a vector embedding for the file.
func (s *Store) Upsert(ctx context.Context, entry models.MemoryEntry, transcriptID string) error {
	key := fmt.Sprintf("memories/%s/%s.md", entry.Category, entry.Name)

	existing, err := s.obj.Get(ctx, key)
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("get existing memory %s: %w", key, err)
	}

	var fm frontmatter
	var body string

	if existing == nil {
		body = entry.Content
	} else {
		fm, _ = parse(existing)

		reconciledBody, reconcileErr := s.llm.ReconcileMemory(ctx, string(existing), entry, transcriptID)
		if reconcileErr != nil {
			// Graceful fallback: append rather than fail the entire transcript.
			log.Printf("ReconcileMemory failed for %s (falling back to append): %v", key, reconcileErr)
			_, existingLines := parse(existing)
			fallbackLines := append(existingLines, "", "---", "", entry.Content)
			body = strings.Join(fallbackLines, "\n")
		} else {
			body = reconciledBody
		}
	}

	fm.Tags = mergeTags(fm.Tags, entry.Tags)
	fm.LastUpdated = time.Now().UTC()
	if !contains(fm.SourceIDs, transcriptID) {
		fm.SourceIDs = append(fm.SourceIDs, transcriptID)
	}

	out, err := render(fm, body)
	if err != nil {
		return err
	}

	if err := s.obj.Put(ctx, key, out); err != nil {
		return err
	}

	// Store embedding if configured. Log and continue on failure so a missing
	// OpenAI key never blocks memory ingestion.
	if s.emb != nil && s.idx != nil {
		vec, embErr := s.emb.Embed(ctx, body)
		if embErr != nil {
			log.Printf("embed %s: %v (skipping)", key, embErr)
		} else if idxErr := s.idx.Upsert(ctx, key, vec); idxErr != nil {
			log.Printf("index embedding %s: %v (skipping)", key, idxErr)
		}
	}

	return nil
}

// List returns all object keys with the given prefix.
func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	if prefix != "" && !strings.HasPrefix(prefix, "memories/") {
		prefix = "memories/" + prefix
	}
	return s.obj.List(ctx, prefix)
}

// Cat returns the raw content of a memory file.
func (s *Store) Cat(ctx context.Context, path string) ([]byte, error) {
	if !strings.HasPrefix(path, "memories/") {
		path = "memories/" + path
	}
	return s.obj.Get(ctx, path)
}

// Grep searches memory files for the given term.
// When semantic=true and an embedder is configured it uses vector similarity;
// otherwise it falls back to case-insensitive substring scan.
func (s *Store) Grep(ctx context.Context, term string, semantic bool) ([]string, error) {
	if semantic && s.emb != nil && s.idx != nil {
		vec, err := s.emb.Embed(ctx, term)
		if err != nil {
			log.Printf("embed query %q: %v (falling back to keyword)", term, err)
		} else {
			return s.idx.Search(ctx, vec, 20)
		}
	}
	return s.obj.GrepAll(ctx, term)
}

func render(fm frontmatter, body string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		return nil, err
	}
	buf.WriteString("---\n")
	buf.WriteString(strings.TrimLeft(body, "\n"))
	return buf.Bytes(), nil
}

func parse(data []byte) (frontmatter, []string) {
	text := string(data)
	var fm frontmatter
	if !strings.HasPrefix(text, "---") {
		return fm, strings.Split(text, "\n")
	}
	parts := strings.SplitN(text, "---", 3)
	if len(parts) < 3 {
		return fm, strings.Split(text, "\n")
	}
	_ = yaml.Unmarshal([]byte(parts[1]), &fm)
	return fm, strings.Split(strings.TrimPrefix(parts[2], "\n"), "\n")
}

func mergeTags(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing))
	result := make([]string, 0, len(existing)+len(incoming))
	for _, t := range existing {
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			result = append(result, t)
		}
	}
	for _, t := range incoming {
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			result = append(result, t)
		}
	}
	return result
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NoSuchKey")
}
