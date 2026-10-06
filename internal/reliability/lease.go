package reliability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
)

// Defaults for worker leases.
const (
	DefaultLeaseDuration     = 30 * time.Second
	DefaultHeartbeatInterval = 10 * time.Second
)

// ErrInvalidConfig rejects unusable reliability settings.
var ErrInvalidConfig = errors.New("reliability: invalid configuration")

// Heartbeat is the execution.LeaseKeeper of a worker. While an attempt runs
// it renews the attempt's lease every Interval, extending it to Duration from
// the renewal (database clock). It stops the attempt (cancels its context)
//
//   - with cause execution.ErrLeaseLost when a renewal reports the lease is
//     no longer held (the attempt was recovered, or finalized by another
//     actor such as a cancellation), and also, self-fencing, when renewals
//     keep failing until the lease could expire: the worker stops before the
//     reaper may hand the attempt to someone else;
//   - with cause execution.ErrCancelRequested when the execution's
//     cancellation was requested (cancellation propagates into the running
//     node through its context).
type Heartbeat struct {
	Store    execution.LeaseStore
	Duration time.Duration
	Interval time.Duration
	Logger   *slog.Logger
	// now is replaceable in tests.
	now func() time.Time
}

var _ execution.LeaseKeeper = (*Heartbeat)(nil)

// NewHeartbeat validates the timing: a lease must survive at least two missed
// heartbeats.
func NewHeartbeat(store execution.LeaseStore, duration, interval time.Duration, logger *slog.Logger) (*Heartbeat, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: heartbeat requires a lease store", ErrInvalidConfig)
	}
	if duration <= 0 || interval <= 0 || duration < 2*interval {
		return nil, fmt.Errorf("%w: lease duration %s must be at least twice the heartbeat interval %s", ErrInvalidConfig, duration, interval)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Heartbeat{Store: store, Duration: duration, Interval: interval, Logger: logger, now: time.Now}, nil
}

// Keep implements execution.LeaseKeeper.
func (h *Heartbeat) Keep(ctx context.Context, executionID, claimToken uuid.UUID) (context.Context, func()) {
	kept, cancel := context.WithCancelCause(ctx)
	now := h.now
	if now == nil {
		now = time.Now
	}
	// The claim granted the lease just before Keep: it is valid for Duration
	// measured from (at the latest) now. Stop one interval before that.
	validUntil := now().Add(h.Duration)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(h.Interval)
		defer ticker.Stop()
		log := h.Logger.With("execution_id", executionID)
		for {
			select {
			case <-kept.Done():
				return
			case <-ticker.C:
			}
			sent := now()
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(kept), h.Interval)
			state, err := h.Store.Renew(rctx, executionID, claimToken, h.Duration)
			rcancel()
			switch {
			case err != nil:
				if !now().Before(validUntil.Add(-h.Interval)) {
					log.Error("lease renewal keeps failing; stopping the attempt before its lease can expire", "error", err)
					cancel(fmt.Errorf("%w: renewals failed until the lease could expire: %v", execution.ErrLeaseLost, err))
					return
				}
				log.Warn("lease renewal failed; retrying at the next heartbeat", "error", err)
			case !state.Held:
				log.Warn("lease lost: the attempt is no longer owned by this worker")
				cancel(execution.ErrLeaseLost)
				return
			default:
				validUntil = sent.Add(h.Duration)
				if state.CancelRequested {
					log.Info("cancellation requested; stopping the attempt")
					cancel(execution.ErrCancelRequested)
					return
				}
			}
		}
	}()
	return kept, func() {
		cancel(context.Canceled)
		wg.Wait()
	}
}
