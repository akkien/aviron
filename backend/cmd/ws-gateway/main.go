package main

import (
	"log"

	"github.com/akkien/aviron/internal/wsgateway"
)

func main() {
	cfg, err := wsgateway.LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	Run(cfg)
}
