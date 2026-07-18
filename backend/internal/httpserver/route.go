package httpserver

import (
	"net/http"

	"github.com/akkien/aviron/internal/auth"
	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	httpSwagger "github.com/swaggo/http-swagger"
)

func RegisterRoutes(server *http.ServeMux, cfg config.Config, pool *pgxpool.Pool) {
	healthzHandler := NewHealthzHandler(pool)
	server.HandleFunc("GET /healthz", healthzHandler)

	authRepo := postgres.NewAuthRepository(pool)
	authSvc := auth.NewService(authRepo)
	authHandler := auth.NewHandler(authSvc)

	server.HandleFunc("POST /auth/register", authHandler.Register)

	server.HandleFunc("GET /swagger/", httpSwagger.WrapHandler)
}
