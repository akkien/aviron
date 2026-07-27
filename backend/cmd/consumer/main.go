package main

import (
	"github.com/akkien/aviron/internal/config"
)

func main() {
	cfg := config.Load()
	Run(cfg)
}
