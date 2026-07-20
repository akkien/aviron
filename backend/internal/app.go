package internal

import (
	"context"
	"log"
	"net/http"

	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/db"
	"github.com/akkien/aviron/internal/httpserver"
	"github.com/akkien/aviron/internal/middleware"
)

func Run(cfg *config.Config) {
	ctx := context.Background()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	server := httpserver.NewServer()
	httpserver.RegisterRoutes(server, *cfg, pool)

	handler := middleware.Cors(cfg.CORSAllowedOrigin)(server)

	log.Printf("listening on :%s", cfg.Port)
	log.Fatal(http.ListenAndServe(":"+cfg.Port, handler))
}
