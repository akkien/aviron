package main

import (
	app "github.com/akkien/aviron/internal"
	"github.com/akkien/aviron/internal/config"
)

func main() {
	cfg := config.Load()
	app.Run(cfg)
}
