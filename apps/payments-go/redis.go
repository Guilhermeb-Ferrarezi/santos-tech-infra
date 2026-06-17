package main

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

func newRedis(url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	// Timeouts de comando/pool: evita goroutines presas se o Redis ficar lento/indisponível.
	opt.DialTimeout = 5 * time.Second
	opt.ReadTimeout = 3 * time.Second
	opt.WriteTimeout = 3 * time.Second
	opt.PoolTimeout = 4 * time.Second
	opt.PoolSize = 10
	c := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return c, nil
}
