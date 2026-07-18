package main

import (
	"context"
	"log"
	"net/http"

	"github.com/akkien/aviron/backend/internal/config"
	"github.com/akkien/aviron/backend/internal/db"
	"github.com/akkien/aviron/backend/internal/httpserver"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	srv := httpserver.NewServer(pool)
	log.Printf("listening on :%s", cfg.Port)
	log.Fatal(http.ListenAndServe(":"+cfg.Port, srv))
}
