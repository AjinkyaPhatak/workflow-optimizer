package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"
)

// ErrUnavailable wraps failures to reach Redis.
var ErrUnavailable = errors.New("redis: unavailable")

// Client owns the process's single Redis connection pool. Create it once per
// process with Open, share it, and Close it on shutdown.
type Client struct {
	rdb       *goredis.Client
	closeOnce sync.Once
	closeErr  error
}

// Open parses a redis:// or rediss:// URL (as in REDIS_URL; credentials, if
// any, come from the URL), and verifies the server answers PING.
func Open(ctx context.Context, url string) (*Client, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("%w: REDIS_URL is empty", ErrUnavailable)
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis: invalid URL: %w", err)
	}
	// Honour context deadlines on individual commands.
	opts.ContextTimeoutEnabled = true
	c := &Client{rdb: goredis.NewClient(opts)}
	if err := c.Ping(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Ping reports whether Redis is reachable (the health check).
func (c *Client) Ping(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

// Close releases all connections. It is safe to call more than once.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.rdb.Close() })
	return c.closeErr
}
