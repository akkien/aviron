package httpserver

import (
	"net/http"

	"github.com/akkien/aviron/internal/auth"
	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/middleware"
	"github.com/akkien/aviron/internal/postgres"
	"github.com/akkien/aviron/internal/race"
	"github.com/jackc/pgx/v5/pgxpool"
	httpSwagger "github.com/swaggo/http-swagger"
)

func RegisterRoutes(server *http.ServeMux, cfg config.Config, pool *pgxpool.Pool) {
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
	raceHandler := race.NewRaceHandler(raceSvc)

	server.Handle("POST /races", requireAuth(http.HandlerFunc(raceHandler.Create)))
	server.Handle("POST /races/{id}/join", requireAuth(http.HandlerFunc(raceHandler.Join)))

	server.HandleFunc("GET /swagger/", httpSwagger.WrapHandler)
}
