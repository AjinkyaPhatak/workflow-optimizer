package retry_test

import (
	"errors"
	"testing"
	"time"

	"workflow-optimizer/internal/retry"
)

func fixed(r float64) func() float64 { return func() float64 { return r } }

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	p := retry.Policy{MaxAttempts: 10, InitialDelay: time.Second, MaxDelay: 10 * time.Second, BackoffMultiplier: 2}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if got := p.Backoff(i + 1); got != w {
			t.Fatalf("Backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
	// Huge attempt numbers must not overflow into a negative or giant delay.
	if got := p.Backoff(10_000); got != 10*time.Second {
		t.Fatalf("Backoff(10000) = %s", got)
	}
	if got := p.Backoff(0); got != time.Second {
		t.Fatalf("Backoff(0) = %s", got)
	}
}

func TestDelayWithoutJitterEqualsBackoff(t *testing.T) {
	p := retry.Policy{MaxAttempts: 5, InitialDelay: 100 * time.Millisecond, MaxDelay: time.Second, BackoffMultiplier: 3}
	for a := 1; a <= 5; a++ {
		if p.Delay(a, 0) != p.Backoff(a) {
			t.Fatalf("attempt %d: %s != %s", a, p.Delay(a, 0), p.Backoff(a))
		}
	}
}

func TestJitterStaysWithinBounds(t *testing.T) {
	p := retry.Policy{MaxAttempts: 5, InitialDelay: time.Second, MaxDelay: time.Minute, BackoffMultiplier: 2, Jitter: 0.5}
	base := p.Backoff(3) // 4s
	if d := (retry.Policy{MaxAttempts: 5, InitialDelay: time.Second, MaxDelay: time.Minute, BackoffMultiplier: 2, Jitter: 0.5, Rand: fixed(0)}).Delay(3, 0); d != base {
		t.Fatalf("r=0 delay %s, want %s", d, base)
	}
	p.Rand = fixed(0.999999)
	if d := p.Delay(3, 0); d < base/2 || d > base {
		t.Fatalf("r~1 delay %s outside [%s, %s]", d, base/2, base)
	}
	// With real randomness every delay stays in [base/2, base] and they differ.
	p.Rand = nil
	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		d := p.Delay(3, 0)
		if d < base/2 || d > base {
			t.Fatalf("jittered delay %s outside [%s, %s]", d, base/2, base)
		}
		seen[d] = true
	}
	if len(seen) < 10 {
		t.Fatalf("jitter produced only %d distinct delays", len(seen))
	}
	// A broken random source cannot push the delay out of range.
	p.Rand = fixed(7)
	if d := p.Delay(3, 0); d != base {
		t.Fatalf("out-of-range random gave %s", d)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	p := retry.Policy{MaxAttempts: 3, InitialDelay: time.Second, MaxDelay: 2 * time.Second, BackoffMultiplier: 2, Jitter: 0.5, Rand: fixed(0.9)}
	// Retry-After above the computed delay wins, even above MaxDelay.
	if d := p.Delay(1, 30*time.Second); d != 30*time.Second {
		t.Fatalf("delay %s, want Retry-After 30s", d)
	}
	// A smaller Retry-After never shortens the backoff.
	if d := p.Delay(2, time.Millisecond); d < time.Second {
		t.Fatalf("delay %s shortened by a small Retry-After", d)
	}
}

func TestAttemptsRemainIsBounded(t *testing.T) {
	p := retry.Default()
	if !p.AttemptsRemain(0) || !p.AttemptsRemain(2) || p.AttemptsRemain(3) || p.AttemptsRemain(4) {
		t.Fatal("default policy must allow exactly 3 attempts")
	}
	if (retry.Policy{MaxAttempts: 1}).AttemptsRemain(1) {
		t.Fatal("MaxAttempts 1 must never retry")
	}
}

func TestValidate(t *testing.T) {
	if err := retry.Default().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []retry.Policy{
		{MaxAttempts: 0, InitialDelay: time.Second, MaxDelay: time.Second, BackoffMultiplier: 2},
		{MaxAttempts: 1, InitialDelay: 0, MaxDelay: time.Second, BackoffMultiplier: 2},
		{MaxAttempts: 1, InitialDelay: 2 * time.Second, MaxDelay: time.Second, BackoffMultiplier: 2},
		{MaxAttempts: 1, InitialDelay: time.Second, MaxDelay: time.Second, BackoffMultiplier: 0.5},
		{MaxAttempts: 1, InitialDelay: time.Second, MaxDelay: time.Second, BackoffMultiplier: 2, Jitter: 1.5},
	}
	for i, p := range bad {
		if err := p.Validate(); !errors.Is(err, retry.ErrInvalidPolicy) {
			t.Errorf("policy %d accepted: %+v", i, p)
		}
	}
}
