package telegram

import (
	"context"
	"testing"
	"time"
)

func TestPacer_EnforcesSpacing(t *testing.T) {
	interval := 10 * time.Millisecond
	p := NewPacer(interval)
	ctx := context.Background()

	start := time.Now()
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < interval {
		t.Errorf("elapsed = %v, want at least %v between two Wait calls", elapsed, interval)
	}
}

func TestPacer_ZeroIntervalNoDelay(t *testing.T) {
	p := NewPacer(0)
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := p.Wait(ctx); err != nil {
			t.Fatalf("Wait %d: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("elapsed = %v, want near-zero with interval=0", elapsed)
	}
}

func TestPacer_RespectsContextCancellation(t *testing.T) {
	p := NewPacer(time.Hour)
	ctx := context.Background()
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := p.Wait(cancelCtx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Wait with canceled context: got nil error, want non-nil")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("Wait with canceled context took %v, want a prompt return", elapsed)
	}
}
