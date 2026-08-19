package crawl

import (
	"testing"
	"time"
)

func TestBackoffExponential(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 30 * time.Second},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{6, 30 * time.Minute}, // 30s<<6 = 32m, capped at 30m
		{100, backoffMax},     // shift capped, stays at max
	}
	for _, c := range cases {
		if got := backoff(c.attempts, 0); got != c.want {
			t.Errorf("backoff(%d,0) = %v, want %v", c.attempts, got, c.want)
		}
	}
}

func TestBackoffHonorsRetryAfter(t *testing.T) {
	// Retry-After wins when it is longer than the exponential delay.
	if got := backoff(0, 5*time.Minute); got != 5*time.Minute {
		t.Errorf("backoff(0, 5m) = %v, want 5m", got)
	}
	// The exponential delay wins when it is longer than Retry-After.
	if got := backoff(3, time.Second); got != 4*time.Minute {
		t.Errorf("backoff(3, 1s) = %v, want 4m", got)
	}
	// A pathologically long Retry-After is capped.
	if got := backoff(0, 2*time.Hour); got != backoffMax {
		t.Errorf("backoff(0, 2h) = %v, want %v", got, backoffMax)
	}
}
