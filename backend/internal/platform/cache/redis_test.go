package cache

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/google/uuid"
)

func TestRedisDisabledAndUnavailableRemainCacheMisses(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		store := NewRedis[string](config.RedisSettings{Enabled: enabled, Address: "127.0.0.1:1", Namespace: "test"})
		defer store.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		value, err := Resolve(ctx, store, "key", func(context.Context) (string, error) { return "database", nil })
		if err != nil || value != "database" {
			t.Fatalf("optional cache affected result: %q %v", value, err)
		}
	}
}

func TestRedisRoundTripWithIsolatedNamespace(t *testing.T) {
	address := os.Getenv("GO_ADMIN_TEST_REDIS_ADDRESS")
	if address == "" {
		t.Skip("GO_ADMIN_TEST_REDIS_ADDRESS is not configured")
	}
	store := NewRedis[[]string](config.RedisSettings{Enabled: true, Address: address, Namespace: "gap-test:" + uuid.NewString()})
	defer store.Close()
	ctx := context.Background()
	defer store.client.Del(ctx, store.namespace+"account:1")
	store.Set(ctx, "account:1", []string{"iam.users.read"})
	value, ok := store.Get(ctx, "account:1")
	if !ok || len(value) != 1 || value[0] != "iam.users.read" {
		t.Fatal("cache round trip failed")
	}
	if _, ok := store.Get(ctx, "account:2"); ok {
		t.Fatal("new authorization version reused stale cache")
	}
}
