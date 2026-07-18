package main

import (
	app "github.com/akkien/aviron/internal"
	"github.com/akkien/aviron/internal/config"

	_ "github.com/akkien/aviron/docs"
)

// @title Aviron Backend API
// @version 1.0
// @description Real-time multiplayer fitness backend (Aviron-inspired). See context/project-overview.md for the full design.
// @BasePath /
func main() {
	cfg := config.Load()
	app.Run(cfg)
}
