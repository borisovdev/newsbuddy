package telegram_test

import (
	"context"
	"errors"
	"testing"

	"github.com/borisovdev/newsbuddy/internal/telegram"
	"github.com/borisovdev/newsbuddy/internal/telegram/tgtest"
)

func TestFakePublisher_SendWithFallback(t *testing.T) {
	cases := []struct {
		name          string
		scripted      error
		plain         string
		wantErr       error
		wantCount     int
		wantPlainSent string
	}{
		{
			name:      "success needs no fallback",
			scripted:  nil,
			plain:     "plain body",
			wantErr:   nil,
			wantCount: 1,
		},
		{
			name:          "parse entities error retries with plain",
			scripted:      tgtest.ErrParseEntities,
			plain:         "plain body",
			wantErr:       nil,
			wantCount:     2,
			wantPlainSent: "plain body",
		},
		{
			name:      "parse entities error with no plain does not retry",
			scripted:  tgtest.ErrParseEntities,
			plain:     "",
			wantErr:   tgtest.ErrParseEntities,
			wantCount: 1,
		},
		{
			name:      "rate limited error does not retry with plain",
			scripted:  tgtest.ErrRateLimited,
			plain:     "plain body",
			wantErr:   tgtest.ErrRateLimited,
			wantCount: 1,
		},
		{
			name:      "forbidden error does not retry with plain",
			scripted:  telegram.ErrForbidden,
			plain:     "plain body",
			wantErr:   telegram.ErrForbidden,
			wantCount: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := tgtest.NewFakePublisher(tc.scripted, nil)
			_, err := fp.SendWithFallback(context.Background(), "<b>html</b>", tc.plain)

			if tc.wantErr == nil && err != nil {
				t.Fatalf("SendWithFallback() error = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("SendWithFallback() error = %v, want %v", err, tc.wantErr)
			}
			if got := fp.Count(); got != tc.wantCount {
				t.Fatalf("Count() = %d, want %d", got, tc.wantCount)
			}
			if tc.wantPlainSent != "" {
				sends := fp.Sends()
				last := sends[len(sends)-1]
				if last.Plain != tc.wantPlainSent {
					t.Errorf("last recorded Plain = %q, want %q", last.Plain, tc.wantPlainSent)
				}
			}
		})
	}
}

func TestFakePublisher_Send_NeverFallsBack(t *testing.T) {
	fp := tgtest.NewFakePublisher(tgtest.ErrParseEntities)
	_, err := fp.Send(context.Background(), "<b>html</b>")

	if !errors.Is(err, tgtest.ErrParseEntities) {
		t.Fatalf("Send() error = %v, want %v", err, tgtest.ErrParseEntities)
	}
	if got := fp.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1 (Send must never fall back to plain)", got)
	}
}
