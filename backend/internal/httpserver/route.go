package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof"

	"github.com/akkien/aviron/internal/auth"
	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/leaderboard"
	"github.com/akkien/aviron/internal/metrics"
	"github.com/akkien/aviron/internal/middleware"
	"github.com/akkien/aviron/internal/postgres"
	"github.com/akkien/aviron/internal/race"
	"github.com/akkien/aviron/internal/room"
	"github.com/jackc/pgx/v5/pgxpool"
	httpSwagger "github.com/swaggo/http-swagger"
)

func RegisterRoutes(server *http.ServeMux, cfg config.Config, pool *pgxpool.Pool, ctx context.Context, registry *room.Registry, logger *slog.Logger, m *metrics.Metrics) {
	healthzHandler := NewHealthzHandler(pool)
	server.HandleFunc("GET /healthz", healthzHandler)

	m.RegisterRoomGauges(registry)
	// Not wrapped in requireAuth or middleware.Cors: a Prometheus scraper
	// carries no JWT and is never called from a browser.
	server.Handle("GET /metrics", m.Handler())

	authRepo := postgres.NewAuthRepository(pool)
	authSvc := auth.NewAuthService(authRepo, []byte(cfg.JWTSecret))
	authHandler := auth.NewAuthHandler(authSvc)

	server.HandleFunc("POST /auth/register", authHandler.Register)
	server.HandleFunc("POST /auth/login", authHandler.Login)

	requireAuth := middleware.Auth([]byte(cfg.JWTSecret))

	raceRepo := postgres.NewRaceRepository(pool)
	raceSvc := race.NewRaceService(raceRepo, []byte(cfg.JWTSecret))
	raceHandler := race.NewRaceHandler(raceSvc, registry, ctx, logger)

	server.Handle("POST /races", requireAuth(http.HandlerFunc(raceHandler.Create)))
	server.Handle("GET /races", requireAuth(http.HandlerFunc(raceHandler.ListOpen)))
	server.Handle("POST /races/{id}/join", requireAuth(http.HandlerFunc(raceHandler.Join)))
	server.Handle("POST /races/{id}/start", requireAuth(http.HandlerFunc(raceHandler.Start)))
	server.Handle("GET /races/{id}/text", requireAuth(http.HandlerFunc(raceHandler.Text)))
	server.Handle("GET /races/{id}", requireAuth(http.HandlerFunc(raceHandler.Status)))

	leaderboardRepo := postgres.NewLeaderboardRepository(pool)
	leaderboardSvc := leaderboard.NewLeaderboardService(leaderboardRepo)
	leaderboardHandler := leaderboard.NewLeaderboardHandler(leaderboardSvc)

	server.Handle("GET /leaderboard/me", requireAuth(http.HandlerFunc(leaderboardHandler.Me)))
	server.Handle("GET /leaderboard", requireAuth(http.HandlerFunc(leaderboardHandler.Top)))

	// GET /ws no longer lives here: race-service no longer terminates
	// WebSocket connections directly (room-service-adapter.md) — that
	// handler code relocated to internal/wsgateway, to be stood up as its
	// own process by ws-gateway.md. Disclosed temporary gap: nothing in
	// this repo serves real WebSocket traffic until that spec ships.

	server.HandleFunc("GET /swagger/", httpSwagger.WrapHandler)

	if cfg.PprofEnabled {
		// Not registered via a blank `import _ "net/http/pprof"`: that
		// package's own init() registers onto http.DefaultServeMux, not
		// the *http.ServeMux this project builds explicitly — a blank
		// import alone would compile clean and silently do nothing.
		// Exactly these 5 handlers are needed: Index's own dispatch
		// already serves /debug/pprof/goroutine, /heap, /allocs, etc. via
		// Go 1.22 ServeMux's trailing-slash subtree matching, the same
		// mechanism GET /swagger/ above already relies on. Unauthenticated
		// and uncors'd, matching GET /metrics's precedent — an
		// operator/tool endpoint, not browser or API traffic. No "GET "
		// method prefix (unlike this file's other routes): pprof.Symbol
		// handles both GET and POST (go tool pprof POSTs to it to resolve
		// symbols), so these are registered unrestricted by method, the
		// same way net/http/pprof's own init() registers them.
		server.HandleFunc("/debug/pprof/", pprof.Index)
		server.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		server.HandleFunc("/debug/pprof/profile", pprof.Profile)
		server.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		server.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
}
