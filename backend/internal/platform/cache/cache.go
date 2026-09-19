// Package cache provides an optional process-local acceleration boundary.
package cache

import (
	"context"
	"errors"
)

var ErrLoaderRequired = errors.New("cache loader is required")

// Cache is deliberately small: absence, disablement, and eviction are all normal cache misses.
type Cache[K comparable, V any] interface {
	Get(context.Context, K) (V, bool)
	Set(context.Context, K, V)
	Clear()
}

// Resolve preserves source-of-truth behavior regardless of cache state.
func Resolve[K comparable, V any](ctx context.Context, store Cache[K, V], key K, loader func(context.Context) (V, error)) (V, error) {
	if loader == nil {
		var zero V
		return zero, ErrLoaderRequired
	}
	if store != nil {
		if value, ok := store.Get(ctx, key); ok {
			return value, nil
		}
	}
	value, err := loader(ctx)
	if err != nil {
		var zero V
		return zero, err
	}
	if store != nil {
		store.Set(ctx, key, value)
	}
	return value, nil
}

type disabled[K comparable, V any] struct{}

// Disabled returns a cache that performs no I/O and always misses.
func Disabled[K comparable, V any]() Cache[K, V] { return disabled[K, V]{} }

func (disabled[K, V]) Get(context.Context, K) (V, bool) {
	var zero V
	return zero, false
}
func (disabled[K, V]) Set(context.Context, K, V) {}
func (disabled[K, V]) Clear()                    {}
