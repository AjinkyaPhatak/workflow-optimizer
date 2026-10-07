package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"workflow-optimizer/internal/oauth"
)

// OAuthStateStore is the oauth.StateStore on Redis (Phase C2): each state is
// one key under prefix with the state's TTL, so it expires on its own, and
// Take reads and deletes it in one MULTI transaction, so it can be taken
// once. Keys are hashes of state tokens; values hold no tokens or codes.
type OAuthStateStore struct {
	client *Client
	prefix string
}

var _ oauth.StateStore = (*OAuthStateStore)(nil)

// NewOAuthStateStore stores states under prefix + ":" + key.
func NewOAuthStateStore(client *Client, prefix string) (*OAuthStateStore, error) {
	if client == nil || prefix == "" {
		return nil, errors.New("redis: OAuth state store needs a client and a key prefix")
	}
	return &OAuthStateStore{client: client, prefix: prefix}, nil
}

func (s *OAuthStateStore) key(k string) string { return s.prefix + ":" + k }

// Save stores a new state; an existing key is never overwritten.
func (s *OAuthStateStore) Save(ctx context.Context, key string, st oauth.State, ttl time.Duration) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	ok, err := s.client.rdb.SetNX(ctx, s.key(key), b, ttl).Result()
	if err != nil {
		return fmt.Errorf("%w: save OAuth state: %w", ErrUnavailable, err)
	}
	if !ok {
		return errors.New("redis: duplicate OAuth state")
	}
	return nil
}

// Take returns and deletes a state atomically.
func (s *OAuthStateStore) Take(ctx context.Context, key string) (oauth.State, error) {
	var get *goredis.StringCmd
	_, err := s.client.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		get = p.Get(ctx, s.key(key))
		p.Del(ctx, s.key(key))
		return nil
	})
	if errors.Is(err, goredis.Nil) || (get != nil && errors.Is(get.Err(), goredis.Nil)) {
		return oauth.State{}, oauth.ErrStateNotFound
	}
	if err != nil {
		return oauth.State{}, fmt.Errorf("%w: take OAuth state: %w", ErrUnavailable, err)
	}
	var st oauth.State
	if err := json.Unmarshal([]byte(get.Val()), &st); err != nil {
		return oauth.State{}, oauth.ErrStateNotFound
	}
	return st, nil
}
