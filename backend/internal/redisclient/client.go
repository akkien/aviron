package redisclient

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// NewClient parses url and returns a connected *redis.Client, mirroring
// internal/db.NewPool's ping-before-returning convention so a bad
// connection fails at startup, not on the first room claim.
func NewClient(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redisclient: parse url: %w", err)
	}

	client := redis.NewClient(opts)

	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("redisclient: ping: %w", err)
	}

	return client, nil
}
