package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/db"
	"github.com/akkien/aviron/internal/httpserver"
	"github.com/akkien/aviron/internal/kafka"
	"github.com/akkien/aviron/internal/metrics"
	"github.com/akkien/aviron/internal/middleware"
	"github.com/akkien/aviron/internal/redisclient"
	"github.com/akkien/aviron/internal/room"
	"github.com/akkien/aviron/internal/roombus"
	"github.com/akkien/aviron/internal/roomlocator"
	"github.com/akkien/aviron/internal/roomrelay"
)

// shutdownTimeout bounds the whole graceful-shutdown sequence — draining
// in-flight REST requests, then waiting for any still-running room
// actors to finish naturally (graceful-shutdown.md's product decision:
// let in-progress races finish rather than force-cancelling them the
// instant SIGTERM arrives).
//
// Corrected after a real rolling-update run
// (multi-instance-k8s-verification.md) found the original 25s budget
// too short for entirely ordinary races, not just extreme ones: this
// project's own default k6 scenario (load/scenarios/race-lifecycle.js)
// uses a 30-word race, which alone averages ~36s to finish at realistic
// telemetry pacing (project-overview.md §4.2's 0.4-2s per word) — comfortably
// longer than the old budget. 2 minutes covers ordinary short-to-medium
// races with real margin. A genuinely long race (e.g. project-overview.md
// §3's own distance_meters: 1000 example) still would not fully survive a
// graceful rollout within this budget — an accepted, disclosed limitation,
// the same category as this project's other bounded scope decisions
// (single Redis/NATS instance), not silently pretended away.
// terminationGracePeriodSeconds must comfortably exceed this; the two
// numbers must agree, confirmed against k8s-race-service-deploy.md's and
// k8s-ws-gateway-deploy.md's actual manifest values, not assumed.
const shutdownTimeout = 2 * time.Minute

// roomDrainPollInterval is how often Run rechecks whether every room
// actor has finished during shutdown — small relative to
// shutdownTimeout so the process doesn't linger once rooms are done.
const roomDrainPollInterval = 250 * time.Millisecond

func Run(cfg *config.Config) {
	// Independent of the shutdown signal below, on purpose: nothing here
	// ever cancels this context, so a SIGTERM never force-ends an
	// in-progress room actor — see waitForRoomsToDrain below for how Run
	// still exits in bounded time regardless.
	ctx := context.Background()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	redisClient, err := redisclient.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("redisclient: %v", err)
	}
	defer redisClient.Close()
	locator := roomlocator.NewLocator(redisClient, cfg.InstanceID)

	producer := kafka.NewProducer(strings.Split(cfg.KafkaBrokers, ","), logger)
	defer producer.Close()

	natsConn, err := nats.Connect(cfg.NATSURL)
	if err != nil {
		log.Fatalf("nats: %v", err)
	}
	defer natsConn.Close()
	bus := roombus.NewNATSRoomBus(roomrelay.NewBus(natsConn), logger)

	m := metrics.NewMetrics()
	registry := room.NewRegistry(logger, m, locator, producer, bus, locator)

	gate := &httpserver.ReadinessGate{}
	server := httpserver.NewServer()
	httpserver.RegisterRoutes(server, *cfg, pool, ctx, registry, logger, m, gate)

	handler := middleware.RequestID()(middleware.RequestLog(logger)(middleware.Cors(cfg.CORSAllowedOrigin)(server)))

	httpSrv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: handler,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		logger.Info("listening", slog.String("port", cfg.Port))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-signalCtx.Done()
	logger.Info("shutdown signal received, marking unready")
	gate.MarkShuttingDown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown did not complete cleanly", slog.Any("error", err))
	}

	waitForRoomsToDrain(shutdownCtx, registry, logger)
	logger.Info("shutdown complete")
}

// waitForRoomsToDrain blocks until every room actor registry is tracking
// has finished on its own — the "let in-progress races finish naturally"
// decision only holds in practice if Run itself stays alive long enough
// for that to happen, since main.go returns (and the process exits) the
// instant Run returns, regardless of any background goroutine still
// running. Returns early if ctx's deadline arrives first — the process
// simply exits at that point, and whatever rooms are still active end
// when Kubernetes sends SIGKILL after terminationGracePeriodSeconds, the
// same accepted, bounded-impact limitation context/feature-history.md
// already discloses for an owning instance crashing outright.
func waitForRoomsToDrain(ctx context.Context, registry *room.Registry, logger *slog.Logger) {
	if registry.Count() == 0 {
		return
	}

	logger.Info("waiting for in-progress races to finish", slog.Int("active_rooms", registry.Count()))

	ticker := time.NewTicker(roomDrainPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if registry.Count() == 0 {
				return
			}
		case <-ctx.Done():
			logger.Warn("shutdown budget exhausted with rooms still active", slog.Int("active_rooms", registry.Count()))
			return
		}
	}
}
