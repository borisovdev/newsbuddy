package cache

import (
	"context"
	"errors"
	"time"
)

// ErrFailing is returned by every FailingCache method.
var ErrFailing = errors.New("cache: unavailable")

// FailingCache is a Cache whose every operation fails. Running the poll
// cycle against it proves that a totally broken cache changes log output
// and nothing else: the DB stays authoritative and no duplicate is posted.
type FailingCache struct{}

func (FailingCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return nil, false, ErrFailing
}

func (FailingCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return ErrFailing
}

func (FailingCache) Delete(ctx context.Context, key string) error { return ErrFailing }

func (FailingCache) Exists(ctx context.Context, key string) (bool, error) {
	return false, ErrFailing
}

func (FailingCache) Close() error { return ErrFailing }
