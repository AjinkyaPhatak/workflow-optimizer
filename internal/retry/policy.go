// Package retry holds the Phase 10 retry policy: bounded attempts and
// exponential backoff with a maximum delay, jitter and Retry-After support.
//
// It is pure computation. It never sleeps and never decides WHETHER a failure
// may be retried: that classification belongs to the error itself
// (execution.ExecutionError.Retryable). Callers persist the resulting delay as
// a schedule (next_attempt_at) instead of waiting on it, so a retry never
// occupies a worker while it backs off.
package retry

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// ErrInvalidPolicy rejects an unusable policy.
var ErrInvalidPolicy = errors.New("retry: invalid policy")

// Defaults used when configuration does not override them.
const (
	DefaultMaxAttempts       = 3
	DefaultInitialDelay      = time.Second
	DefaultMaxDelay          = 5 * time.Minute
	DefaultBackoffMultiplier = 2.0
	// DefaultJitter randomizes away up to half of each delay, spreading
	// retries of many executions that failed together (thundering herd).
	DefaultJitter = 0.5
)

// Policy bounds and paces repeated attempts.
type Policy struct {
	// MaxAttempts is the total number of attempts, the first one included
	// (1 = never retry).
	MaxAttempts int
	// InitialDelay is the backoff before the second attempt.
	InitialDelay time.Duration
	// MaxDelay caps the computed backoff (a server's Retry-After may ask for
	// longer and is honoured).
	MaxDelay time.Duration
	// BackoffMultiplier grows the delay after each failed attempt (>= 1).
	BackoffMultiplier float64
	// Jitter in [0, 1] is the fraction of each computed delay that is
	// randomized: the delay is drawn uniformly from [d*(1-Jitter), d].
	Jitter float64
	// Rand returns a value in [0, 1); nil uses math/rand. Tests inject it.
	Rand func() float64
}

// Default returns the default policy.
func Default() Policy {
	return Policy{
		MaxAttempts:       DefaultMaxAttempts,
		InitialDelay:      DefaultInitialDelay,
		MaxDelay:          DefaultMaxDelay,
		BackoffMultiplier: DefaultBackoffMultiplier,
		Jitter:            DefaultJitter,
	}
}

// Validate reports whether the policy is usable.
func (p Policy) Validate() error {
	switch {
	case p.MaxAttempts < 1:
		return fmt.Errorf("%w: max attempts must be at least 1, got %d", ErrInvalidPolicy, p.MaxAttempts)
	case p.InitialDelay <= 0:
		return fmt.Errorf("%w: initial delay must be positive, got %s", ErrInvalidPolicy, p.InitialDelay)
	case p.MaxDelay < p.InitialDelay:
		return fmt.Errorf("%w: max delay %s is below the initial delay %s", ErrInvalidPolicy, p.MaxDelay, p.InitialDelay)
	case p.BackoffMultiplier < 1 || math.IsNaN(p.BackoffMultiplier) || math.IsInf(p.BackoffMultiplier, 0):
		return fmt.Errorf("%w: backoff multiplier must be >= 1, got %v", ErrInvalidPolicy, p.BackoffMultiplier)
	case p.Jitter < 0 || p.Jitter > 1 || math.IsNaN(p.Jitter):
		return fmt.Errorf("%w: jitter must be within [0, 1], got %v", ErrInvalidPolicy, p.Jitter)
	}
	return nil
}

// AttemptsRemain reports whether another attempt may follow `attempts`
// completed attempts.
func (p Policy) AttemptsRemain(attempts int) bool { return attempts < p.MaxAttempts }

// Backoff returns the deterministic (un-jittered) delay after `attempt`
// failed attempts (attempt >= 1): InitialDelay * Multiplier^(attempt-1),
// capped at MaxDelay.
func (p Policy) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := float64(p.InitialDelay) * math.Pow(p.BackoffMultiplier, float64(attempt-1))
	if math.IsInf(d, 0) || math.IsNaN(d) || d > float64(p.MaxDelay) {
		return p.MaxDelay
	}
	return time.Duration(d)
}

// Delay returns how long to wait before the next attempt after `attempt`
// failed attempts: the jittered backoff, but never less than retryAfter (a
// server's explicit instruction, which may exceed MaxDelay).
func (p Policy) Delay(attempt int, retryAfter time.Duration) time.Duration {
	d := p.Backoff(attempt)
	if p.Jitter > 0 {
		r := p.random()
		d -= time.Duration(float64(d) * p.Jitter * r)
	}
	if d < 0 {
		d = 0
	}
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

func (p Policy) random() float64 {
	var r float64
	if p.Rand != nil {
		r = p.Rand()
	} else {
		r = rand.Float64()
	}
	if r < 0 || r >= 1 || math.IsNaN(r) {
		return 0
	}
	return r
}
