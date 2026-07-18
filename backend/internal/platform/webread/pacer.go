// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"context"
	"sync"
	"time"
)

const (
	// pacerMaxConcurrent and pacerMinInterval are the burst budget toward
	// one site: at most twelve requests in flight, request STARTS at least
	// 25ms apart. A deep read is a one-shot, bounded read of one company's
	// site (page/byte/wall caps), not sustained crawling — the founder set
	// the target at "the whole site in under five seconds" (2026-07-18),
	// and a browser page-load fans out harder than this burst does.
	// Robots.txt honor is unchanged.
	pacerMaxConcurrent = 12
	pacerMinInterval   = 25 * time.Millisecond
)

// Pacer paces one crawl's requests to the site it reads. The crawler holds
// one per crawl: the budget belongs to the target site, not to the process.
// Safe for concurrent use.
type Pacer struct {
	slots chan struct{}

	mu        sync.Mutex
	lastStart time.Time

	// now and sleep are seams so pacing is provable without a real clock.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// NewPacer builds a real-clock pacer.
func NewPacer() *Pacer {
	return &Pacer{
		slots: make(chan struct{}, pacerMaxConcurrent),
		now:   time.Now,
		sleep: sleepCtx,
	}
}

// Wait blocks until a request may start: a concurrency slot is free AND the
// minimum interval since the previous start has passed. The caller MUST call
// Done once the request finishes. A context cancellation unblocks Wait with
// the context's error and leaves no slot held.
func (p *Pacer) Wait(ctx context.Context) error {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		p.mu.Lock()
		wait := pacerMinInterval - p.now().Sub(p.lastStart)
		if wait <= 0 {
			p.lastStart = p.now()
			p.mu.Unlock()
			return nil
		}
		p.mu.Unlock()
		// Loop rather than trust one sleep: a concurrent Wait may have taken
		// the start slot this sleep was aiming for.
		if err := p.sleep(ctx, wait); err != nil {
			<-p.slots
			return err
		}
	}
}

// Done releases the concurrency slot Wait acquired.
func (p *Pacer) Done() {
	<-p.slots
}

// sleepCtx is the production sleep: a timer that a context cancellation cuts
// short.
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
