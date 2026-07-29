package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/akkien/aviron/internal/redisclient"
	"github.com/akkien/aviron/internal/roomlocator"
	"github.com/akkien/aviron/internal/roomrelay"
	"github.com/akkien/aviron/internal/wsgateway"
)

// shutdownTimeout bounds how long Shutdown waits for in-flight ServeHTTP
// calls (REST proxy requests and WebSocket connections alike) to return
// once SIGTERM arrives — comfortably inside Kubernetes' default 30s
// terminationGracePeriodSeconds; the two must agree once
// k8s-ws-gateway-deploy.md sets the manifest side.
const shutdownTimeout = 25 * time.Second

// connFlushWindow is a short pause between marking this gateway unready
// and force-disconnecting every local WebSocket connection
// (graceful-shutdown.md) — long enough to let any broadcast already in
// flight over the bus reach a raceHub's fan-out and get written out,
// rather than racing a connection's disconnect against its own final
// message.
const connFlushWindow = 500 * time.Millisecond

func Run(cfg wsgateway.Config) {
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	redisClient, err := redisclient.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("redisclient: %v", err)
	}
	defer redisClient.Close()

	// "ws-gateway" is inert here — this process never calls
	// Claim/Refresh/Release/MarkEvicted (all race-service-only), only
	// Owner/SubscribeRoomEvents/IsEvicted.
	locator := roomlocator.NewLocator(redisClient, "ws-gateway")

	natsConn, err := nats.Connect(cfg.NATSURL)
	if err != nil {
		log.Fatalf("nats: %v", err)
	}
	defer natsConn.Close()
	relay := roomrelay.NewBus(natsConn)

	gw := wsgateway.NewGateway(locator, cfg.Backends, cfg.CacheTTL, logger)
	go gw.WatchRoomEvents(ctx)

	hubs := wsgateway.NewRaceHubRegistry(ctx, relay, logger)
	wsHandler := wsgateway.NewWSHandler(locator, relay, hubs, cfg.JWTSecret, cfg.AllowedOrigin, logger)

	gate := &wsgateway.ReadinessGate{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", wsgateway.NewHealthzHandler(redisClient, natsConn, gate))
	mux.HandleFunc("GET /livez", wsgateway.NewLivezHandler())
	mux.Handle("GET /ws", wsHandler)
	mux.Handle("/", gw)

	httpSrv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		logger.Info("ws-gateway listening", slog.String("addr", cfg.ListenAddr), slog.Any("backends", cfg.Backends))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-signalCtx.Done()
	logger.Info("shutdown signal received, marking unready")
	gate.MarkShuttingDown()

	// Give any already-in-flight broadcast a moment to reach local
	// connections before force-disconnecting them below — see
	// connFlushWindow's own comment.
	time.Sleep(connFlushWindow)

	// Force-disconnects every locally-held WebSocket connection first, not
	// after: httpSrv.Shutdown below blocks until every in-flight
	// ServeHTTP call returns, and a WSHandler's call only returns once its
	// connection actually closes — calling Shutdown before this would
	// deadlock, waiting on connections nothing has told to close yet.
	hubs.Shutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown did not complete cleanly", slog.Any("error", err))
	} else {
		logger.Info("shutdown complete")
	}
}
