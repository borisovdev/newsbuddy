// Package cache holds every piece of caching state in news-buddy. The map
// internals of the in-memory implementation and of KeySet never escape this
// package; callers see string keys and []byte values only.
package cache

import (
	"context"
	"time"
)

// Cache is deliberately Redis-shaped: string keys, []byte values, every
// operation takes a context and can fail. A Redis
// implementation is a drop-in; no caller changes.
type Cache interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	Close() error
}
