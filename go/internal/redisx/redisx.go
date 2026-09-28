// Package redisx wraps Redis token locks.
package redisx

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	RDB *redis.Client
}

func Dial(addr string) (*Client, error) {
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	return &Client{RDB: rdb}, nil
}

func (c *Client) Ping(ctx context.Context) error { return c.RDB.Ping(ctx).Err() }

func (c *Client) Close() error { return c.RDB.Close() }

// Acquire tries SET NX PX. Returns token ownership bool.
func (c *Client) Acquire(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	ok, err := c.RDB.SetNX(ctx, key, token, ttl).Result()
	return ok, err
}

var unlockScript = redis.NewScript(`if redis.call("get",KEYS[1]) == ARGV[1] then return redis.call("del",KEYS[1]) else return 0 end`)

// Release deletes only if token matches.
func (c *Client) Release(ctx context.Context, key, token string) error {
	return unlockScript.Run(ctx, c.RDB, []string{key}, token).Err()
}
