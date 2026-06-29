package handler

import (
	"encoding/json"
	"net/http"
	"strings"

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
	if data == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// Grep searches all memory files for the given term.
// GET /memories/search?q=golang
// GET /memories/search?q=golang&mode=semantic  (uses pgvector similarity when available)
func (h *MemoryHandler) Grep(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}
	semantic := strings.EqualFold(r.URL.Query().Get("mode"), "semantic")
	matches, err := h.store.Grep(r.Context(), q, semantic)
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
	return err != nil && (strings.Contains(err.Error(), "NoSuchKey") || strings.Contains(err.Error(), "not found"))
}
