package maintenance

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestLocalTargetsOnly(t *testing.T) {
	for _, raw := range []string{"postgres://u:p@localhost:55432/local", "redis://127.0.0.1:56379/0", "postgres://[::1]:5432/test"} {
		if err := ValidateLocalURL(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"postgres://remote.test/db", "redis://10.0.0.1/0", "postgres:///test", "not a url"} {
		if ValidateLocalURL(raw) == nil {
			t.Errorf("accepted remote or invalid target %s", raw)
		}
	}
	if ValidateLocalAddress("127.0.0.1:57233") != nil || ValidateLocalAddress("remote.test:7233") == nil {
		t.Fatal("Temporal address boundary")
	}
}
func TestTemporalOwnershipExcludesKnowledgeAndOtherStacks(t *testing.T) {
	id := uuid.NewString()
	cases := []struct {
		id, kind, queue string
		owned           bool
	}{
		{"workflow-" + id, "workflow.v2", "our-queue", true},
		{"trigger-" + id, "automation.trigger", "our-queue", true},
		{"scheduled-" + id + "-2026-10-27T00:00:00Z", "automation.trigger", "our-queue", true},
		{"workflow-" + id, "knowledge.index", "our-queue", false},
		{"workflow-" + id, "workflow.v2", "other-queue", false},
		{"unrelated-" + id, "workflow.v2", "our-queue", false},
	}
	for _, tc := range cases {
		if got := OwnedWorkflow(tc.id, tc.kind, tc.queue, "our-queue"); got != tc.owned {
			t.Errorf("%s/%s/%s = %v", tc.id, tc.kind, tc.queue, got)
		}
	}
}
func TestRedisCleanupOnlyOwnedIDs(t *testing.T) {
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL is required")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := redis.NewClient(options)
	defer r.Close()
	ctx := context.Background()
	prefix := "maintenance-test:" + uuid.NewString()
	queue, dead, oauth := prefix+":queue", prefix+":dead", prefix+":oauth"
	t.Cleanup(func() { r.Del(ctx, queue, dead, oauth) })
	owned, foreign := uuid.NewString(), uuid.NewString()
	r.RPush(ctx, queue, `{"v":1,"execution_id":"`+owned+`"}`, `{"v":1,"execution_id":"`+foreign+`"}`, "malformed")
	r.RPush(ctx, dead, `{"v":1,"execution_id":"`+owned+`","reason":"attempts_exhausted"}`)
	r.Set(ctx, oauth, "keep", 0)
	removed, err := CleanRedis(ctx, r, queue, dead, []string{owned})
	if err != nil || removed != 2 {
		t.Fatalf("cleanup %d %v", removed, err)
	}
	values, err := r.LRange(ctx, queue, 0, -1).Result()
	if err != nil || len(values) != 2 {
		t.Fatalf("foreign entries: %v %v", values, err)
	}
	if value := r.Get(ctx, oauth).Val(); value != "keep" {
		t.Fatal("unrelated Redis state changed")
	}
}
