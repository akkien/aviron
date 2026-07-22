package httpserver

import (
	"context"
	"net/http"

	"github.com/akkien/aviron/internal/auth"
	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/leaderboard"
	"github.com/akkien/aviron/internal/middleware"
	"github.com/akkien/aviron/internal/postgres"
	"github.com/akkien/aviron/internal/race"
	"github.com/akkien/aviron/internal/room"
	"github.com/akkien/aviron/internal/ws"
	"github.com/jackc/pgx/v5/pgxpool"
	httpSwagger "github.com/swaggo/http-swagger"
)

func RegisterRoutes(server *http.ServeMux, cfg config.Config, pool *pgxpool.Pool, ctx context.Context, registry *room.Registry) {
	healthzHandler := NewHealthzHandler(pool)
	server.HandleFunc("GET /healthz", healthzHandler)

	authRepo := postgres.NewAuthRepository(pool)
	authSvc := auth.NewAuthService(authRepo, []byte(cfg.JWTSecret))
	authHandler := auth.NewAuthHandler(authSvc)

	server.HandleFunc("POST /auth/register", authHandler.Register)
	server.HandleFunc("POST /auth/login", authHandler.Login)

	requireAuth := middleware.Auth([]byte(cfg.JWTSecret))

	raceRepo := postgres.NewRaceRepository(pool)
	raceSvc := race.NewRaceService(raceRepo, []byte(cfg.JWTSecret))
	raceHandler := race.NewRaceHandler(raceSvc, registry, ctx)

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

	// Not wrapped in requireAuth: this endpoint authenticates via the
	// query-string session_token (websocket/ws-endpoint.md), not the
	// Authorization header middleware.Auth expects.
	wsHandler := ws.NewWSHandler(registry, []byte(cfg.JWTSecret), cfg.CORSAllowedOrigin)
	server.Handle("GET /ws", wsHandler)

	server.HandleFunc("GET /swagger/", httpSwagger.WrapHandler)
}
