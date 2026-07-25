package racerouter

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/akkien/aviron/internal/redisclient"
	"github.com/akkien/aviron/internal/roomlocator"
)

// Run connects to Redis, builds the Router, starts its room:events
// subscription, and serves — blocking forever with context.Background(),
// matching cmd/server's existing model. No SIGTERM/graceful-shutdown
// handling: none exists anywhere else in this codebase to mirror (confirmed
// during this feature's own grounding, despite race-router.md's spec text
// assuming one does), so this stays consistent with cmd/server rather than
// inventing a new pattern for just this binary.
func Run(cfg Config) {
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	client, err := redisclient.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("redisclient: %v", err)
	}
	defer client.Close()

	// "race-router" is inert here — this process never calls
	// Claim/Refresh/Release, only Owner/SubscribeRoomEvents.
	locator := roomlocator.NewLocator(client, "race-router")

	router := NewRouter(locator, cfg.Backends, cfg.CacheTTL, logger)
	go router.watchRoomEvents(ctx)

	logger.Info("race-router listening", slog.String("addr", cfg.ListenAddr), slog.Any("backends", cfg.Backends))
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, router))
}
