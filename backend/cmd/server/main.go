// Command server runs the Guest Score API.
//
// It also serves the compiled React SPA when STATIC_DIR points at one, so a
// production image is a single binary with no nginx sidecar and no CORS.
//
// Everything external is optional. With no environment set at all this starts
// on the JSON file store, computes scores in-process, caches nothing, searches
// by substring and ingests no events — which is the same demo the repository
// has always run. Each variable in internal/config swaps one of those for the
// real thing, independently, and the boot log says which way each one resolved.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/api"
	"github.com/udaykishore-resu/guest-score/backend/internal/cache"
	"github.com/udaykishore-resu/guest-score/backend/internal/config"
	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	"github.com/udaykishore-resu/guest-score/backend/internal/events"
	"github.com/udaykishore-resu/guest-score/backend/internal/graphqlapi"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoringsvc"
	"github.com/udaykishore-resu/guest-score/backend/internal/search"
	"github.com/udaykishore-resu/guest-score/backend/internal/store"
	"github.com/udaykishore-resu/guest-score/backend/internal/store/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// seeder is implemented by both stores. Seeding is not part of store.Store
// because a production store should not be obliged to know how to fabricate
// data.
type seeder interface {
	IsEmpty() bool
	LoadSeed(guests []domain.Guest, reviews []domain.Review)
}

func run() error {
	var (
		staticDir = flag.String("static", os.Getenv("STATIC_DIR"), "directory of built SPA assets to serve; empty disables")
		reseed    = flag.Bool("reseed", false, "wipe the file snapshot and regenerate demo data on start")
		healthURL = flag.String("health", "", "probe this URL and exit; for a distroless container healthcheck")
	)
	flag.Parse()

	if *healthURL != "" {
		return probe(*healthURL)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	// Boot resolution first, before anything can fail: if the process dies on a
	// bad DSN, the log already says which store it was trying to open.
	log.Info("guest-score starting", cfg.Summary()...)
	for _, wmsg := range cfg.Warnings() {
		log.Warn(wmsg)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// closers run in reverse order on the way out.
	var closers []func()
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}()

	// --- store ---------------------------------------------------------------
	var (
		st        store.Store
		sd        seeder
		pgStore   *postgres.Store
		fileStore *store.FileStore
	)
	if cfg.Postgres.Enabled() {
		pgStore, err = postgres.Open(ctx, postgres.Options{
			DSN: cfg.Postgres.DSN, MaxConns: cfg.Postgres.MaxConns,
			Migrate: cfg.Postgres.Migrate, Log: log,
		})
		if err != nil {
			// Fatal, not degraded: the operator explicitly configured a database,
			// and silently running on a JSON file instead would mean writes land
			// somewhere nobody is looking.
			return fmt.Errorf("opening postgres store: %w", err)
		}
		st, sd = pgStore, pgStore
		closers = append(closers, func() { _ = pgStore.Close() })
	} else {
		if *reseed && cfg.DataPath != "" {
			if err := os.Remove(cfg.DataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("clearing snapshot: %w", err)
			}
			log.Info("snapshot cleared", "path", cfg.DataPath)
		}
		fileStore, err = store.NewFileStore(cfg.DataPath)
		if err != nil {
			return fmt.Errorf("opening file store: %w", err)
		}
		st, sd = fileStore, fileStore
		closers = append(closers, func() { _ = fileStore.Close() })
	}

	if cfg.Seed && sd.IsEmpty() {
		guests, reviews := store.Seed(time.Now().UTC())
		sd.LoadSeed(guests, reviews)
		if fileStore != nil {
			if err := fileStore.Flush(); err != nil {
				log.Warn("initial snapshot failed", "err", err)
			}
			log.Info("seeded demo dataset", "guests", len(guests), "reviews", len(reviews))
		}
	}

	// --- cache ---------------------------------------------------------------
	var c cache.Cache = cache.Nop{}
	var redisCache *cache.Redis
	if cfg.Redis.Enabled() {
		redisCache, err = cache.NewRedis(ctx, cache.RedisOptions{
			Addr: cfg.Redis.Addr, Password: cfg.Redis.Password,
			DB: cfg.Redis.DB, Namespace: "gs", Log: log,
		})
		if err != nil {
			return fmt.Errorf("connecting to redis: %w", err)
		}
		c = redisCache
		closers = append(closers, func() { _ = redisCache.Close() })
	}

	// --- search --------------------------------------------------------------
	var idx search.Index = search.Nop{}
	var elastic *search.Elastic
	if cfg.Elastic.Enabled() {
		elastic, err = search.NewElastic(ctx, search.ElasticOptions{
			URL: cfg.Elastic.URL, Index: cfg.Elastic.Index,
			Username: cfg.Elastic.Username, Password: cfg.Elastic.Password, Log: log,
		})
		if err != nil {
			return fmt.Errorf("connecting to elasticsearch: %w", err)
		}
		if err := elastic.Ensure(ctx); err != nil {
			return fmt.Errorf("preparing search index: %w", err)
		}
		idx = elastic
		closers = append(closers, func() { _ = elastic.Close() })
	}

	// --- scoring -------------------------------------------------------------
	var scorer scoringsvc.Scorer = scoringsvc.NewLocal(scoring.DefaultModel)
	var remote *scoringsvc.Remote
	if cfg.Scoring.Remote() {
		remote, err = scoringsvc.NewRemote(cfg.Scoring.GRPCTarget, scoring.DefaultModel, cfg.Scoring.Timeout, log)
		if err != nil {
			return fmt.Errorf("dialling the scoring service: %w", err)
		}
		scorer = remote
		closers = append(closers, func() { _ = remote.Close() })
	}

	// --- API -----------------------------------------------------------------
	opts := []api.Option{
		api.WithIdentityKey(cfg.IdentityKey),
		api.WithScorer(scorer),
		api.WithCache(c),
		api.WithSearch(idx),
		api.WithHealthChecks(healthChecks(pgStore, redisCache, elastic, remote)...),
	}

	srv := api.New(st, log, opts...)

	if cfg.GraphQL.Enabled {
		schema, err := graphqlapi.New(graphqlapi.Deps{
			Store: st, Scorer: scorer, Search: idx, Now: scoring.Now,
		})
		if err != nil {
			return fmt.Errorf("building the GraphQL schema: %w", err)
		}
		srv = api.New(st, log, append(opts,
			api.WithGraphQL(graphqlapi.WithTimeout(
				graphqlapi.Handler(schema, cfg.GraphQL.GraphiQL), 20*time.Second)),
		)...)
	}

	// --- MQTT ingest ---------------------------------------------------------
	if cfg.MQTT.Enabled() {
		var dd events.Deduper = events.NewMemoryDeduper()
		if pgStore != nil {
			// Durable deduplication: an in-memory one forgets across a restart,
			// which is exactly when a QoS 1 broker redelivers.
			dd = pgStore
		} else {
			log.Warn("MQTT deduplication is in-memory because no database is configured; " +
				"a redelivery across a restart will be applied twice")
		}
		ingest, err := events.New(events.Options{
			URL: cfg.MQTT.URL, ClientID: cfg.MQTT.ClientID, Topic: cfg.MQTT.Topic,
			Username: cfg.MQTT.Username, Password: cfg.MQTT.Password,
			Store: st, Deduper: dd, Log: log,
		})
		if err != nil {
			return fmt.Errorf("connecting to the MQTT broker: %w", err)
		}
		closers = append(closers, func() { _ = ingest.Close() })
	}

	// --- HTTP ----------------------------------------------------------------
	mux := http.NewServeMux()
	routes := srv.Routes()
	mux.Handle("/api/", routes)
	mux.Handle("/graphql", routes)
	mux.Handle("/graphiql", routes)
	if *staticDir != "" {
		mux.Handle("/", spaHandler(*staticDir, log))
		log.Info("serving SPA", "dir", *staticDir)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/api/health", http.StatusTemporaryRedirect)
		})
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // a reindex is synchronous and slow
		IdleTimeout:       120 * time.Second,
	}

	idle := make(chan struct{})
	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			log.Error("graceful shutdown failed", "err", err)
		}
		close(idle)
	}()

	log.Info("guest-score listening", "addr", cfg.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-idle
	log.Info("stopped")
	return nil
}

// healthChecks assembles the dependency probes.
//
// Only the database is critical. A cache, a search index and a scoring sidecar
// each have a working fallback, so failing readiness on them would take a
// correct service out of rotation — the opposite of what a probe is for.
func healthChecks(pg *postgres.Store, rc *cache.Redis, es *search.Elastic, remote *scoringsvc.Remote) []api.HealthCheck {
	var checks []api.HealthCheck
	if pg != nil {
		checks = append(checks, api.HealthCheck{Name: "postgres", Critical: true, Probe: pg.Ping})
	}
	if rc != nil {
		checks = append(checks, api.HealthCheck{Name: "redis", Probe: rc.Ping})
	}
	if es != nil {
		checks = append(checks, api.HealthCheck{Name: "elasticsearch", Probe: es.Ping})
	}
	if remote != nil {
		checks = append(checks, api.HealthCheck{Name: "scoring-grpc", Probe: remote.Ping})
	}
	return checks
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

// probe is the container healthcheck. A distroless image has no shell and no
// curl, so the binary checks itself.
func probe(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("health probe returned %s", resp.Status)
	}
	return nil
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
