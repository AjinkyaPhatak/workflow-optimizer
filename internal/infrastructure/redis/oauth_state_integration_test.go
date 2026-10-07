package redis_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	redisinfra "workflow-optimizer/internal/infrastructure/redis"
	"workflow-optimizer/internal/oauth"
)

func stateStore(t *testing.T) (*redisinfra.OAuthStateStore, *goredis.Client, string) {
	t.Helper()
	url := redisURL(t)
	c, err := redisinfra.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	prefix := "test:phasec2:" + uuid.NewString()
	s, err := redisinfra.NewOAuthStateStore(c, prefix)
	if err != nil {
		t.Fatal(err)
	}
	opts, _ := goredis.ParseURL(url)
	raw := goredis.NewClient(opts)
	t.Cleanup(func() {
		keys, _ := raw.Keys(context.Background(), prefix+"*").Result()
		if len(keys) > 0 {
			_ = raw.Del(context.Background(), keys...).Err()
		}
		_ = raw.Close()
	})
	return s, raw, prefix
}

func TestRedisOAuthStateIsSingleUseAndExpires(t *testing.T) {
	s, raw, prefix := stateStore(t)
	ctx := context.Background()
	st := oauth.State{Provider: "fake_oauth", WorkspaceID: uuid.New(), UserID: uuid.New(), Flow: oauth.FlowConnect,
		BindingHash: "h", CodeVerifier: "v", ExpiresAt: time.Now().Add(time.Minute).UTC().Truncate(time.Second)}
	token := "state-token-RAW-VALUE"
	key := oauth.StateKey(token)
	if err := s.Save(ctx, key, st, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, key, st, time.Minute); err == nil {
		t.Fatal("a state key is never overwritten")
	}
	// Only the hash is a key, and the value holds no raw state token.
	keys, _ := raw.Keys(ctx, prefix+"*").Result()
	if len(keys) != 1 || strings.Contains(keys[0], token) {
		t.Fatalf("keys: %v", keys)
	}
	if ttl, _ := raw.TTL(ctx, keys[0]).Result(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("ttl %v", ttl)
	}
	// Ten concurrent takes: exactly one wins.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.Take(ctx, key)
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
				if got.WorkspaceID != st.WorkspaceID || !got.ExpiresAt.Equal(st.ExpiresAt) || got.CodeVerifier != "v" {
					t.Errorf("state: %+v", got)
				}
			} else if !errors.Is(err, oauth.ErrStateNotFound) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d takes succeeded", wins)
	}
	// Expiry: Redis drops the state on its own.
	if err := s.Save(ctx, "short", st, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := s.Take(ctx, "short"); !errors.Is(err, oauth.ErrStateNotFound) {
		t.Fatalf("expired state: %v", err)
	}
}
