package cache

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/redis/go-redis/v9"
)

// Redis 只加速可重建投影。所有故障均按缓存未命中处理，禁止承担鉴权或持久化职责。
type Redis[V any] struct {
	client    *redis.Client
	namespace string
}

func NewRedis[V any](settings config.RedisSettings) *Redis[V] {
	store := &Redis[V]{namespace: settings.Namespace + ":"}
	if !settings.Enabled {
		return store
	}
	options := &redis.Options{Addr: settings.Address, Username: settings.Username, Password: settings.Password, DB: settings.Database,
		DialTimeout: 100 * time.Millisecond, ReadTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond, PoolTimeout: 100 * time.Millisecond, MaxRetries: -1, ContextTimeoutEnabled: true}
	if settings.TLS {
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	store.client = redis.NewClient(options)
	return store
}

func (store *Redis[V]) Get(ctx context.Context, key string) (V, bool) {
	var value V
	if store == nil || store.client == nil {
		return value, false
	}
	ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	data, err := store.client.Get(ctx, store.namespace+key).Bytes()
	if err != nil || json.Unmarshal(data, &value) != nil {
		return value, false
	}
	return value, true
}
func (store *Redis[V]) Set(ctx context.Context, key string, value V) {
	if store == nil || store.client == nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	_ = store.client.Set(ctx, store.namespace+key, data, 5*time.Minute).Err()
}

// Clear 不删除共享 Redis 中其他实例的数据；投影失效由数据库版本号驱动。
func (store *Redis[V]) Clear() {}
func (store *Redis[V]) Close() error {
	if store == nil || store.client == nil {
		return nil
	}
	return store.client.Close()
}
