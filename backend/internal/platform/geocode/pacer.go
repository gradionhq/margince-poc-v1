// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package geocode

import (
	"context"
	"sync"
	"time"
)

// Pacer holds this installation to one geocoding request per interval.
//
// It differs from webread.Pacer in the one way that matters: there is NO
// concurrency, deliberately. The site reader paces per target site and may run
// eight in flight, because the budget belongs to each site. Nominatim's policy
// asks a recurring client to be single-threaded against the ONE service every
// request goes to, so the whole installation is one requester and Wait
// serializes it.
//
// Safe for concurrent use; concurrent callers queue.
type Pacer struct {
	mu        sync.Mutex
	interval  time.Duration
	lastStart time.Time

	// now and sleep are seams so the pacing is provable without a real clock —
	// a test that had to wait 15 seconds to prove a 15-second floor would be
	// skipped, and a skipped test proves nothing.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// NewPacer builds a real-clock pacer at the given floor.
func NewPacer(interval time.Duration) *Pacer {
	return &Pacer{interval: interval, now: time.Now, sleep: sleepCtx}
}

// Wait blocks until this installation may make its next request.
//
// It holds the lock ACROSS the sleep, which is what makes the pacer a queue
// rather than a suggestion: releasing it first would let every waiting caller
// compute the same deadline and start together, which is precisely the burst
// the interval exists to prevent.
func (p *Pacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.lastStart.IsZero() {
		if wait := p.interval - p.now().Sub(p.lastStart); wait > 0 {
			if err := p.sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	p.lastStart = p.now()
	return nil
}

// sleepCtx sleeps, or gives up when the caller does — a request nobody is
// waiting for any more must not hold the queue.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
