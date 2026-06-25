CREATE TABLE IF NOT EXISTS transcripts (
    id          TEXT PRIMARY KEY,
    content     TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'pending',
    error       TEXT        NOT NULL DEFAULT '',
    attempts    INTEGER     NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_transcripts_status ON transcripts (status);
