package memstore_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/storage"
	"github.com/borisovdev/newsbuddy/internal/storage/memstore"
	"github.com/borisovdev/newsbuddy/internal/storage/storagetest"
)

func TestMemstoreContract(t *testing.T) {
	storagetest.RunContractTests(t, func(t *testing.T) storage.Storage {
		return memstore.New()
	})
}

// TestConcurrentInsertPending exercises the dedup gate from many goroutines
// at once, under -race, to check the single RWMutex actually serializes
// writers and InsertPending's read-then-append stays atomic.
func TestConcurrentInsertPending(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()

	src, err := store.AddSource(ctx, model.Source{FeedURL: "https://example.com/concurrent.xml", Title: "Concurrent"}, nil)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	const goroutines = 50
	const keysPerGoroutine = 20

	var insertedCount int64
	var mu sync.Mutex
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := 0; k < keysPerGoroutine; k++ {
				// Every goroutine writes the same set of keys so most calls
				// race on the same rows and only one call per key should win.
				key := fmt.Sprintf("item-%d", k)
				inserted, err := store.InsertPending(ctx, src.ID, key)
				if err != nil {
					t.Errorf("InsertPending(%s): %v", key, err)
					return
				}
				if inserted {
					mu.Lock()
					insertedCount++
					mu.Unlock()
				}
			}
		}(g)
	}
	wg.Wait()

	if insertedCount != keysPerGoroutine {
		t.Fatalf("total inserted = %d, want %d (exactly one winner per key)", insertedCount, keysPerGoroutine)
	}

	unseen, err := store.FilterUnseen(ctx, src.ID, []string{"item-0", "item-1", "not-inserted"})
	if err != nil {
		t.Fatalf("FilterUnseen: %v", err)
	}
	if len(unseen) != 1 || unseen[0] != "not-inserted" {
		t.Fatalf("FilterUnseen = %v, want only [not-inserted]", unseen)
	}
}
