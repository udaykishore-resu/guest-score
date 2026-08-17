// Command scoringd serves the bureau's scoring model over gRPC.
//
// It holds no data and talks to nothing: records arrive in the request, a score
// goes back, and the process has no store, no cache and no clock of its own
// beyond the fallback. That is what makes it safe to scale to any number of
// replicas and safe to redeploy independently of the API — which is the whole
// point of separating it, since the model is the part that gets audited and
// versioned.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	pb "github.com/udaykishore-resu/guest-score/backend/internal/gen/scoringv1"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoringsvc"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", envOr("GS_GRPC_ADDR", ":9090"), "gRPC listen address")
	flag.Parse()

	var level slog.Level
	if err := level.UnmarshalText([]byte(envOr("GS_LOG_LEVEL", "info"))); err != nil {
		level = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", *addr, err)
	}

	srv := grpc.NewServer(
		// A batch carries every review for every guest on a directory page, so
		// the default 4 MiB receive limit is reachable with a large page. 16 MiB
		// is generous without being an invitation.
		grpc.MaxRecvMsgSize(16<<20),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			// Close idle connections so a rolling API deploy does not leave
			// half-open connections pinned to a draining replica.
			MaxConnectionIdle: 5 * time.Minute,
			Time:              30 * time.Second,
			Timeout:           10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			// The API's client pings every 30s; without this the server would
			// treat that as abusive and tear the connection down.
			MinTime:             20 * time.Second,
			PermitWithoutStream: true,
		}),
	)

	pb.RegisterScoringServiceServer(srv, scoringsvc.NewServer(scoring.DefaultModel, log))

	// The Kubernetes native gRPC probe speaks grpc.health.v1, so this is what
	// readiness actually calls — not a synthetic HTTP port.
	hs := health.NewServer()
	hs.SetServingStatus("scoring.v1.ScoringService", healthpb.HealthCheckResponse_SERVING)
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, hs)

	// Reflection lets grpcurl explore the service without a .proto file, which
	// is what makes this demoable from a terminal. It also exposes the full
	// schema, so it belongs behind the same network boundary as the service.
	reflection.Register(srv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		// Flip health to NOT_SERVING before draining: Kubernetes stops sending
		// new connections while in-flight calls finish, instead of racing.
		hs.SetServingStatus("scoring.v1.ScoringService", healthpb.HealthCheckResponse_NOT_SERVING)
		hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

		graceful := make(chan struct{})
		go func() { srv.GracefulStop(); close(graceful) }()
		select {
		case <-graceful:
		case <-time.After(15 * time.Second):
			log.Warn("graceful stop timed out, forcing")
			srv.Stop()
		}
		close(done)
	}()

	log.Info("scoring service listening",
		"addr", *addr, "model_version", scoringsvc.ModelVersion)
	if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	<-done
	log.Info("stopped")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
