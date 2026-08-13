// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package licensecheck

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// recheckInterval is how often a running process re-resolves its posture. A
// license changes state on a calendar — expiry, then expiry plus grace — so
// daily is as often as the answer can differ, and a re-check costs one
// short-lived wazero instantiation.
const recheckInterval = 24 * time.Hour

// Watcher holds this installation's license posture and re-resolves it while the
// process runs, so crossing expiry-plus-grace takes effect without a restart.
//
// It never ends the process. The boot gate is where a refused license stops an
// installation; a process that is already serving degrades instead, because a
// licensing edge case must not take a customer's CRM offline mid-month with no
// human in the loop.
type Watcher struct {
	token   string
	now     func() time.Time
	log     *slog.Logger
	posture atomic.Pointer[Posture]
}

// NewWatcher resolves the posture once and refuses a rejected one, so the
// caller's boot fails before the role serves. An absent license is not a
// refusal: an unlicensed installation runs.
func NewWatcher(ctx context.Context, token string, now func() time.Time, log *slog.Logger) (*Watcher, error) {
	w := &Watcher{token: token, now: now, log: log}
	resolved := Resolve(ctx, token, now())
	if resolved.State == StateRejected {
		// Where the token CAME from is not named here: this package is handed a
		// token, not a configuration file, and the caller that resolved it is the
		// one that can tell the operator which setting to correct.
		return nil, fmt.Errorf("licensecheck: the license was refused by the bundled validation module (%s): %s",
			ModuleVersion(), resolved.Reason)
	}
	w.posture.Store(&resolved)
	return w, nil
}

// Posture answers the most recent resolution. Safe for concurrent readers: the
// pointer is swapped whole, so a reader sees one answer or the next and never a
// mixture.
func (w *Watcher) Posture() Posture { return *w.posture.Load() }

// Recheck resolves once and records the answer, reporting a state that CHANGED.
// A steady state is not logged: an operator reading a year of logs should find
// the day the license lapsed, not three hundred and sixty-five lines saying it
// had not.
func (w *Watcher) Recheck(ctx context.Context) {
	before := w.Posture()
	after := Resolve(ctx, w.token, w.now())
	w.posture.Store(&after)
	if after.State == before.State {
		return
	}
	// Any transition is worth a warning, including one back to valid: a license
	// that recovered did so because somebody changed something, and the record of
	// when it recovered is as operationally useful as when it lapsed.
	w.log.WarnContext(ctx, "license posture changed",
		"from", string(before.State), "to", string(after.State),
		"reason", after.Reason, "module", ModuleVersion())
}

// RunRecheck re-resolves until ctx is cancelled. It is started by each process
// role that resolved a posture at boot; nothing else drives it.
func (w *Watcher) RunRecheck(ctx context.Context) {
	ticker := time.NewTicker(recheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Recheck(ctx)
		}
	}
}
