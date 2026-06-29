package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pgvector/pgvector-go"
)

type EmbeddingStore struct {
	db *sql.DB
}

func NewEmbeddingStore(db *sql.DB) *EmbeddingStore {
	return &EmbeddingStore{db: db}
}

// Upsert stores or replaces the embedding vector for a memory file path.
func (s *EmbeddingStore) Upsert(ctx context.Context, path string, embedding []float32) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO memory_embeddings (path, embedding, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (path) DO UPDATE SET embedding = $2, updated_at = NOW()`,
		path, pgvector.NewVector(embedding),
	)
	if err != nil {
		return fmt.Errorf("upsert embedding %s: %w", path, err)
	}
	return nil
}

// Search returns up to limit memory file paths ordered by cosine similarity
// to the given query embedding.
func (s *EmbeddingStore) Search(ctx context.Context, query []float32, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT path FROM memory_embeddings
		ORDER BY embedding <=> $1
		LIMIT $2`,
		pgvector.NewVector(query), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
