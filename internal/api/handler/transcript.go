package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/Codex-AK/memory-wiki/internal/models"
	"github.com/Codex-AK/memory-wiki/internal/storage/db"
)

type TranscriptHandler struct {
	store *db.TranscriptStore
}

func NewTranscriptHandler(store *db.TranscriptStore) *TranscriptHandler {
	return &TranscriptHandler{store: store}
}

func (h *TranscriptHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	body.Content = strings.TrimSpace(body.Content)
	if body.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	t := &models.Transcript{
		ID:        uuid.NewString(),
		Content:   body.Content,
		Status:    models.StatusPending,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := h.store.Create(t); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(t)
}

func (h *TranscriptHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := h.store.GetByID(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if t == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}
