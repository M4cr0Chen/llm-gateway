package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// NewRedis constructs a Redis client from a DSN. It pings the server to
// fail fast on misconfiguration. The caller owns the returned client and
// must call Close on shutdown. Accepts both redis:// and rediss:// URLs.
func NewRedis(ctx context.Context, dsn string) (*redis.Client, error) {
	if dsn == "" {
		return nil, fmt.Errorf("redis dsn is empty")
	}

	opts, err := redis.ParseURL(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing redis dsn: %w", err)
	}

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("pinging redis: %w", err)
	}
	return client, nil
}
