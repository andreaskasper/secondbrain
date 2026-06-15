// Command secondbrain is a fast, path-based REST document store for markdown
// files, designed so that Claude and other agents can quickly read, write and
// search a "second brain" knowledge base.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	store, err := NewStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("cannot initialise data dir %q: %v", cfg.DataDir, err)
	}

	app := &App{store: store, cfg: cfg}

	mux := http.NewServeMux()

	// Health check: no authentication, used by the Docker healthcheck.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Everything else goes through auth + the document store.
	mux.Handle("/", auth(cfg.APIKey, app))

	handler := logging(mux)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("secondbrain listening on %s, data dir %s", cfg.Addr, store.Root)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
		os.Exit(1)
	}
	log.Println("bye")
}
