package db

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/lib/pq"
)

func Connect() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL not set")
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return conn, nil
}

func Migrate(conn *sql.DB) error {
	query := `
CREATE TABLE IF NOT EXISTS transcripts (
    id          TEXT PRIMARY KEY,
    content     TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'pending',
    error       TEXT        NOT NULL DEFAULT '',
    attempts    INTEGER     NOT NULL DEFAULT 0,
    retry_after TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE transcripts ADD COLUMN IF NOT EXISTS retry_after TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_transcripts_status ON transcripts (status);
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE IF NOT EXISTS memory_embeddings (
    path        TEXT PRIMARY KEY,
    embedding   vector(1536),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`
	_, err := conn.Exec(query)
	return err
}
