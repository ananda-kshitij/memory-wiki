package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"

	"github.com/Codex-AK/memory-wiki/internal/api/handler"
	"github.com/Codex-AK/memory-wiki/internal/api/middleware"
	"github.com/Codex-AK/memory-wiki/internal/llm"
	memstore "github.com/Codex-AK/memory-wiki/internal/memory"
	"github.com/Codex-AK/memory-wiki/internal/storage/db"
	"github.com/Codex-AK/memory-wiki/internal/storage/object"
	"github.com/Codex-AK/memory-wiki/internal/worker"
)

func main() {
	_ = godotenv.Load()

	conn, err := db.Connect()
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer conn.Close()

	if err := db.Migrate(conn); err != nil {
		log.Fatalf("db migrate: %v", err)
	}

	objClient, err := object.New()
	if err != nil {
		log.Fatalf("object store: %v", err)
	}

	transcriptStore := db.NewTranscriptStore(conn)
	memStore := memstore.NewStore(objClient)
	llmClient := llm.New()

	w := worker.New(transcriptStore, llmClient, memStore)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	r := chi.NewRouter()
	r.Use(chimiddleware.Recoverer)
	r.Use(middleware.Logger)

	th := handler.NewTranscriptHandler(transcriptStore)
	mh := handler.NewMemoryHandler(memStore)

	r.Post("/transcripts", th.Create)
	r.Get("/transcripts/{id}", th.Get)

	r.Get("/memories", mh.Ls)
	r.Get("/memories/search", mh.Grep)
	r.Get("/memories/{category}/{name}", mh.Cat)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("server listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
