package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/nats-io/nats.go"

	"github.com/akkien/aviron/internal/redisclient"
	"github.com/akkien/aviron/internal/roomlocator"
	"github.com/akkien/aviron/internal/roomrelay"
	"github.com/akkien/aviron/internal/wsgateway"
)

// Run connects to Redis and NATS, builds the Gateway and WSHandler, starts
// the routing cache's room:events subscription, and serves — blocking
// forever with context.Background(), matching cmd/server's/the deleted
// cmd/race-router's existing model. No SIGTERM/graceful-shutdown handling,
// consistent with every other binary in this codebase (confirmed during
// race-router.md's own grounding that no such pattern exists anywhere to
// mirror).
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", wsgateway.NewHealthzHandler(redisClient, natsConn))
	mux.Handle("GET /ws", wsHandler)
	mux.Handle("/", gw)

	logger.Info("ws-gateway listening", slog.String("addr", cfg.ListenAddr), slog.Any("backends", cfg.Backends))
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}
