package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/Codex-AK/memory-wiki/internal/memory"
)

type MemoryHandler struct {
	store *memory.Store
}

func NewMemoryHandler(store *memory.Store) *MemoryHandler {
	return &MemoryHandler{store: store}
}

// Ls lists all memory files under an optional prefix.
// GET /memories?prefix=people/
func (h *MemoryHandler) Ls(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	keys, err := h.store.List(r.Context(), prefix)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if keys == nil {
		keys = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]string{"files": keys})
}

// Cat returns the raw content of a memory file.
// GET /memories/{category}/{name}
func (h *MemoryHandler) Cat(w http.ResponseWriter, r *http.Request) {
	category := chi.URLParam(r, "category")
	name := chi.URLParam(r, "name")
	path := category + "/" + name + ".md"

	data, err := h.store.Cat(r.Context(), path)
	if err != nil {
		if isNotFound(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// Grep searches all memory files for the given term.
// GET /memories/search?q=golang
func (h *MemoryHandler) Grep(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}
	matches, err := h.store.Grep(r.Context(), q)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if matches == nil {
		matches = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]string{"matches": matches})
}

func isNotFound(err error) bool {
	return err != nil && (contains(err.Error(), "NoSuchKey") || contains(err.Error(), "not found"))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
