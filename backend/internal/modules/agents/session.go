// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The handshake era's session bookkeeping: what `initialize` mints, what
// `DELETE /mcp` closes, and the two caps that keep the structure bounded.
//
// It is one file on purpose. A modern call carries its own state and mints
// nothing here, so this is the whole of what the older framing needs and the
// newer one does not — and it is what C2 removes once the per-Passport volume
// counters that currently ride on it are live (ADR-0092 §6).

import (
	"sync"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// sessionKey identifies one live MCP session. The session id ALONE is
// deliberately not enough to act on (DESIGN §10.4): every request
// re-authenticates via the Bearer passport, which is where authority
// comes from, so the id itself is unvalidated. Pairing it with the
// presenting passport means DELETE can only ever close a session that
// passport itself opened — keying on the id alone would let any
// authenticated agent close another connector's session by guessing or
// replaying its value.
type sessionKey struct {
	passportID ids.UUID
	sessionID  string
}

// The two caps that make the registry a BOUNDED structure. Without them
// `initialize` (240/min per passport at the edge) grows it forever: nothing but
// an exact-match DELETE ever removed an entry, a client that crashes or drops
// its connection never sends one, and every refresh rotation brings a fresh
// passport with a fresh allowance of its own. The symptom of the unbounded
// version is an api whose memory climbs until it is restarted, with no metric
// naming the cause.
//
// Per passport, because a client legitimately holds ONE session: the cap is
// above one only so a client reconnecting before its DELETE lands is not
// squeezed, and the OLDEST entry gives way rather than the newest being
// refused — the newest is the session the client is actually using.
//
// Across the whole registry, because the passport dimension is otherwise
// unbounded on its own. The per-passport cap is what keeps one credential from
// evicting everyone else's sessions to reach the global one.
const (
	maxSessionsPerPassport = 8
	maxSessions            = 4096
)

// sessionRegistry is in-process bookkeeping for open MCP sessions,
// scoped to ONE handler instance rather than a package-level global —
// a global would leak state between two handlers (or two tests) in the
// same process.
//
// The value is the insertion SEQUENCE, which is what "evict the oldest" reads:
// a counter rather than a timestamp, so eviction order is exact and needs no
// clock to be deterministic in a test.
type sessionRegistry struct {
	mu       sync.Mutex
	sessions map[sessionKey]uint64
	inserted uint64
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: make(map[sessionKey]uint64)}
}

// register records a new session under the presenting passport, evicting
// whatever the caps above require. An evicted entry only ever costs its owner a
// 404 on a DELETE it may never send.
func (r *sessionRegistry) register(passportID ids.UUID, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictForLocked(passportID)
	r.inserted++
	r.sessions[sessionKey{passportID, sessionID}] = r.inserted
}

// evictForLocked makes room for one more session under passportID: its own
// oldest goes when that passport is at its cap, and otherwise the registry's
// oldest goes when the whole map is at its. Both scans walk the map, which is
// bounded by maxSessions by construction.
func (r *sessionRegistry) evictForLocked(passportID ids.UUID) {
	if r.evictOldestLocked(func(key sessionKey) bool { return key.passportID == passportID },
		maxSessionsPerPassport) {
		return
	}
	r.evictOldestLocked(func(sessionKey) bool { return true }, maxSessions)
}

// evictOldestLocked drops the lowest-sequence entry matching `counts` if at
// least `limit` entries match it, and reports whether it evicted anything.
func (r *sessionRegistry) evictOldestLocked(counts func(sessionKey) bool, limit int) bool {
	matching := 0
	var oldest sessionKey
	oldestAt := uint64(0)
	for key, at := range r.sessions {
		if !counts(key) {
			continue
		}
		matching++
		if oldestAt == 0 || at < oldestAt {
			oldest, oldestAt = key, at
		}
	}
	if matching < limit {
		return false
	}
	delete(r.sessions, oldest)
	return true
}

// close removes the session sessionID owned by passportID. It reports
// false, leaving the registry untouched, when no session exists under
// that exact pair — including a sessionID that IS live but under a
// different passport. Those two cases must read identically: telling
// them apart would let a DELETE probe confirm another connector's
// session id is currently open.
func (r *sessionRegistry) close(passportID ids.UUID, sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := sessionKey{passportID, sessionID}
	if _, ok := r.sessions[key]; !ok {
		return false
	}
	delete(r.sessions, key)
	return true
}
