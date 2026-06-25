package models

import "time"

// MemoryFile represents a parsed memory file from object storage.
type MemoryFile struct {
	Path        string    `json:"path"`
	Category    string    `json:"category"`
	Name        string    `json:"name"`
	Tags        []string  `json:"tags"`
	LastUpdated time.Time `json:"last_updated"`
	SourceIDs   []string  `json:"source_transcript_ids"`
	Content     string    `json:"content"`
}

// MemoryEntry is a single extracted fact produced by the LLM.
type MemoryEntry struct {
	Category string   `json:"category"` // e.g. "people", "topics", "projects"
	Name     string   `json:"name"`     // file stem, e.g. "alice", "machine-learning"
	Tags     []string `json:"tags"`
	Content  string   `json:"content"` // markdown body
}
