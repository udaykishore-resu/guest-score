// Command server runs the Guest Score API and, in production builds, serves
// the compiled React SPA from the same binary.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/api"
	"github.com/udaykishore-resu/guest-score/backend/internal/store"
)

func main() {
	var (
		addr     = flag.String("addr", envOr("ADDR", ":8080"), "listen address")
		dataPath = flag.String("data", envOr("DATA_PATH", "./data/guest-score.json"), "path to the JSON snapshot")
		staticDir = flag.String("static", envOr("STATIC_DIR", ""), "directory of built SPA assets to serve; empty disables")
		reseed   = flag.Bool("reseed", false, "wipe the snapshot and regenerate demo data on start")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *reseed && *dataPath != "" {
		if err := os.Remove(*dataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Error("could not remove snapshot", "path", *dataPath, "err", err)
			os.Exit(1)
		}
		log.Info("snapshot cleared", "path", *dataPath)
	}

	st, err := store.NewFileStore(*dataPath)
	if err != nil {
		log.Error("could not open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// FR-014: an empty store seeds itself, so a fresh clone shows a populated
	// product with no provisioning step.
	if st.IsEmpty() {
		guests, reviews := store.Seed(time.Now().UTC())
		st.LoadSeed(guests, reviews)
		if err := st.Flush(); err != nil {
			log.Warn("initial snapshot failed", "err", err)
		}
		log.Info("seeded demo dataset", "guests", len(guests), "reviews", len(reviews))
	}

	srv := api.New(st, log)

	mux := http.NewServeMux()
	mux.Handle("/api/", srv.Routes())
	if *staticDir != "" {
		mux.Handle("/", spaHandler(*staticDir, log))
		log.Info("serving SPA", "dir", *staticDir)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/api/health", http.StatusTemporaryRedirect)
		})
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown: stop accepting, drain in-flight requests, then let the
	// deferred store.Close write a final snapshot. Without this, a deploy
	// rollover can lose up to one flush interval of reviews.
	idle := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			log.Error("graceful shutdown failed", "err", err)
		}
		close(idle)
	}()

	log.Info("guest-score listening", "addr", *addr, "data", *dataPath)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
	<-idle
	log.Info("stopped")
}

// spaHandler serves static files and falls back to index.html for any path
// that is not a real file, so client-side routes survive a hard refresh.
func spaHandler(dir string, log *slog.Logger) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Clean(r.URL.Path)
		// filepath.Clean already resolves "..", and rejecting any residual
		// parent reference keeps a crafted path from escaping the static dir.
		if strings.Contains(clean, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		candidate := filepath.Join(dir, clean)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			// Hashed build assets are immutable; index.html must never be
			// cached or users get a stale shell pointing at deleted bundles.
			if strings.HasPrefix(clean, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fs.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
