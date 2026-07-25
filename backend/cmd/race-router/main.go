package main

import (
	"log"

	"github.com/akkien/aviron/internal/racerouter"
)

func main() {
	cfg, err := racerouter.LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	racerouter.Run(cfg)
}
