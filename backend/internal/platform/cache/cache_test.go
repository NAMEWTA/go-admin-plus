package cache_test

import (
	"context"
	"errors"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/cache"
)

func TestDisabledCachePreservesSourceOfTruthSemantics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	loads := 0
	loader := func(context.Context) (string, error) {
		loads++
		return "authoritative", nil
	}
	disabled := cache.Disabled[string, string]()

	for range 2 {
		value, err := cache.Resolve(ctx, disabled, "key", loader)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if value != "authoritative" {
			t.Fatalf("Resolve() = %q, want authoritative", value)
		}
	}
	if loads != 2 {
		t.Fatalf("loader calls = %d, want 2", loads)
	}
}

func TestNilCacheIsAMissAndNilLoaderIsStableError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	value, err := cache.Resolve[string, int](ctx, nil, "answer", func(context.Context) (int, error) { return 42, nil })
	if err != nil || value != 42 {
		t.Fatalf("Resolve(nil cache) = (%d, %v), want (42, nil)", value, err)
	}
	if _, err := cache.Resolve[string, int](ctx, nil, "answer", nil); !errors.Is(err, cache.ErrLoaderRequired) {
		t.Fatalf("Resolve(nil loader) error = %v, want ErrLoaderRequired", err)
	}
}

func TestClearableTestCacheDoesNotOwnCorrectness(t *testing.T) {
	t.Parallel()

	store := &fakeCache[string, int]{values: make(map[string]int)}
	loads := 0
	loader := func(context.Context) (int, error) { loads++; return 42, nil }
	for range 2 {
		if _, err := cache.Resolve(context.Background(), store, "answer", loader); err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
	}
	store.Clear()
	if _, err := cache.Resolve(context.Background(), store, "answer", loader); err != nil {
		t.Fatalf("Resolve() after Clear error = %v", err)
	}
	if loads != 2 {
		t.Fatalf("loader calls = %d, want 2", loads)
	}
}

type fakeCache[K comparable, V any] struct{ values map[K]V }

func (f *fakeCache[K, V]) Get(_ context.Context, key K) (V, bool) {
	value, ok := f.values[key]
	return value, ok
}
func (f *fakeCache[K, V]) Set(_ context.Context, key K, value V) { f.values[key] = value }
func (f *fakeCache[K, V]) Clear()                                { f.values = make(map[K]V) }
