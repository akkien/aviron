package main

import (
	"github.com/akkien/aviron/internal/telegramrelay"
)

func main() {
	cfg := telegramrelay.LoadConfig()
	Run(cfg)
}
