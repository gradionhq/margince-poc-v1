// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package readmeter is the MCP-SESS-READS consumption meter
// (api-rate-limits-and-abuse §2.2): how many RECORDS an agent has been handed
// inside one window, and whether that has passed the threshold at which a
// human must confirm before it is handed any more.
//
// PER RECORD, NOT PER CALL. That is the whole control. The spec's own wording
// is that the bound exists "so a single search_records returning 5,000 rows
// trips it — closing the obvious evasion", and A139 repeats it for the brief:
// metered per call, a densely-joined read is the cheapest bulk read on the
// surface and the step-up threshold never sees it.
//
// PER PASSPORT, NOT PER SESSION, which departs from the name. ADR-0055 made a
// Passport a REST credential governed exactly like MCP, and a REST call has no
// session to count against — so a per-session counter would meter one half of
// one surface. ADR-0092/A141 ratifies per-Passport read/write/egress/cost
// counters as the forward shape and the session registry is removed with it,
// so keying on the Passport is the key that survives that change rather than
// the one it would have to undo. The window is a fixed one with expiry, in the
// same mechanism (and the same spelling) as platform/overlaybudget.
//
// FAIL-CLOSED. With no Redis, or a Redis that errors, the meter cannot know
// whether the threshold has been passed — and a control that cannot answer
// must not answer "no". It reports the threshold as passed, and the read is
// refused. The consequence is deliberate and worth stating plainly: while the
// counter is unreachable, agent reads stop. Human sessions never enter this
// path; their authority is RBAC at the store.
//
// It lives in the platform tier and owns no domain: both doors onto the
// governed surface charge it — the MCP tool dispatcher and the ADR-0055 REST
// agent gate — and neither may import the other.
package readmeter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DefaultLimit is MCP-SESS-READS: the records a Passport may be handed inside
// one window before the next read demands human confirmation
// (api-rate-limits-and-abuse §2.2). It is a default and mode-tunable, and §4.2
// makes lowering it below a floor an ADR matter — it is a security control,
// not a performance knob.
const DefaultLimit = 2000

// DefaultWindow is the span one counter covers. The spec names the bound after
// a session; ADR-0055 and ADR-0092 leave the Passport as the only thing both
// doors share, so the Passport's rolling day is what "session" resolves to
// here. It is stated in the tool copy an agent reads, so the surface and the
// operator use the same words.
const DefaultWindow = 24 * time.Hour

// keyPrefix is the namespace every read counter shares. Named because a writer
// that spelled it differently would leave counters nothing else can find.
const keyPrefix = "msr:"

// Meter counts records served to one Passport inside a window. The zero value
// is not usable; construct with New or NewWithClock.
type Meter struct {
	rdb    *redis.Client
	limit  int
	window time.Duration
	now    func() time.Time
}

// New constructs a Meter over rdb using the real wall clock. A nil rdb is
// allowed and makes the meter fail-closed — every read reports the threshold
// passed, because a counter that cannot be read cannot show headroom.
func New(rdb *redis.Client, limit int, window time.Duration) *Meter {
	return NewWithClock(rdb, limit, window, time.Now)
}

// NewWithClock takes the clock as a dependency so the fixed window a charge
// lands in is asserted by advancing time rather than by sleeping against the
// real one (T11): the clock's reading picks both the Redis key and the expiry,
// deterministically.
func NewWithClock(rdb *redis.Client, limit int, window time.Duration, now func() time.Time) *Meter {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if window <= 0 {
		window = DefaultWindow
	}
	return &Meter{rdb: rdb, limit: limit, window: window, now: now}
}

// RebindFrom copies src's client and configuration onto this meter — the
// boot-time injection point compose uses WITHOUT itself naming a Redis client
// (that dependency stays in the cmd/platform tiers). newServer constructs a
// fail-closed meter and shares that ONE pointer with every charge point; a
// WithReadMeter option rebinds it from the live meter once the Redis client
// and the deployment config are known, so every holder sees the live meter
// without re-plumbing. Called at server assembly, before any request is
// served, so it never races a charge — the same discipline
// overlaybudget.Meter.RebindFrom follows.
func (m *Meter) RebindFrom(src *Meter) {
	m.rdb, m.limit, m.window, m.now = src.rdb, src.limit, src.window, src.now
}

// Limit is the configured threshold, for the refusal envelope to report.
func (m *Meter) Limit() int { return m.limit }

// Reading is what the meter knows about one Passport's window.
type Reading struct {
	// Observed is the records served in this window so far.
	Observed int
	// Limit is the threshold Observed is judged against, including any
	// headroom a human has already granted this window.
	Limit int
	// Exceeded reports that the next read needs human confirmation. It is a
	// field rather than a comparison the caller makes, so the fail-closed
	// answer cannot be reconstructed wrongly by a second caller.
	Exceeded bool
}

// countKey is one agent's counter for one window bucket. The bucket comes
// from the injected clock, never Redis TIME, so rollover is deterministic
// under test.
func (m *Meter) countKey(ws ids.UUID, agent string, bucket int64) string {
	return fmt.Sprintf(keyPrefix+"%s:%s:reads:%d", ws.String(), agent, bucket)
}

// grantKey holds the extra headroom a human granted for this window by
// approving a step-up. It is a SEPARATE key from the count deliberately:
// resetting the count would erase the evidence of how much this agent has
// read, and the audit question "how many records did it see" must stay
// answerable after the human said continue.
func (m *Meter) grantKey(ws ids.UUID, agent string, bucket int64) string {
	return fmt.Sprintf(keyPrefix+"%s:%s:grant:%d", ws.String(), agent, bucket)
}

// addScript adds n to a window counter and returns the new total, setting the
// fixed-window expiry on FIRST write only — so the window is fixed rather than
// sliding, and a busy Passport's counter does not renew itself into never
// resetting. Both counters this package keeps (the count and the granted
// headroom) have exactly this shape. ARGV=[n, ttlSeconds].
var addScript = redis.NewScript(`
local n = tonumber(ARGV[1])
local total = redis.call('INCRBY', KEYS[1], n)
if total == n then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[2])) end
return total`)

// meteredAgent names the counter one call belongs to, and reports whether this
// caller is inside the control at all.
//
// ONLY an agent is metered. A human's authority is RBAC at the store and they
// answered for the action themselves; this bound exists to make an AGENT's bulk
// reading visible to the human who granted it. That is the same line
// auth.Gate.Admit already draws, drawn again here so the meter is safe to call
// from anywhere rather than only from behind the gate.
//
// The key is the Passport, and falls back to the principal's own id when there
// is none. Every agent principal this product mints carries a Passport
// (identity.AgentIdentity.Principal), so the fallback is not a live path — but
// keying on something ALWAYS present is what stops a future agent principal
// without one from being silently exempt. An agent that cannot be identified at
// all is not a caller this meter can bound, and says so by returning false,
// which the read side turns into a refusal rather than a free pass.
func (m *Meter) meteredAgent(ctx context.Context) (ws ids.UUID, agent string, metered bool) {
	actor, present := principal.Actor(ctx)
	if !present || actor.Type != principal.PrincipalAgent {
		return ws, "", false
	}
	if actor.PassportID != (ids.UUID{}) {
		agent = actor.PassportID.String()
	} else {
		agent = actor.ID
	}
	ws, bound := principal.WorkspaceID(ctx)
	return ws, agent, bound && agent != ""
}

// usable reports that the meter can actually reach its counter for this call.
func (m *Meter) usable(ctx context.Context) (ws ids.UUID, agent string, ok bool) {
	ws, agent, metered := m.meteredAgent(ctx)
	return ws, agent, metered && m.rdb != nil
}

// bucket is the fixed window a moment falls in.
func (m *Meter) bucket() int64 {
	return m.now().UTC().Unix() / int64(m.window.Seconds())
}

// Consume records n records served to ctx's Passport.
//
// It records UNCONDITIONALLY and never refuses: the spec's mechanism is that
// crossing the threshold refuses the NEXT read call, not that it truncates the
// answer in flight (§2.4). Records already selected have already been read;
// pretending otherwise by dropping them would under-count the exposure the
// meter exists to measure.
//
// A non-positive n is a no-op — an empty page served nothing. A call with no
// Passport (a human, or the system) is not metered and records nothing.
func (m *Meter) Consume(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	ws, agent, ok := m.usable(ctx)
	if !ok {
		return nil
	}
	if err := m.add(ctx, m.countKey(ws, agent, m.bucket()), n); err != nil {
		return fmt.Errorf("readmeter: recording %d served records: %w", n, err)
	}
	return nil
}

// add applies one counter's increment and its first-write expiry.
func (m *Meter) add(ctx context.Context, key string, n int) error {
	return addScript.Run(ctx, m.rdb, []string{key}, n, m.ttlSeconds()).Err()
}

// Grant adds n records of headroom for the rest of the current window — what
// an approved step-up buys. The count itself is untouched, so the answer to
// "how many records has this agent been handed" survives the grant.
func (m *Meter) Grant(ctx context.Context, n int) error {
	if n <= 0 {
		return fmt.Errorf("readmeter: a step-up grant must be positive, got %d", n)
	}
	ws, agent, ok := m.usable(ctx)
	if !ok {
		return fmt.Errorf("readmeter: no metered agent on this call to grant headroom to")
	}
	if err := m.add(ctx, m.grantKey(ws, agent, m.bucket()), n); err != nil {
		return fmt.Errorf("readmeter: granting %d records of headroom: %w", n, err)
	}
	return nil
}

// Read answers what the meter knows about ctx's agent before a read tool
// serves anything.
//
// The two "no" answers are different and must not be folded together:
//
//   - NOT METERED — a human, the system, a call with no actor. These are
//     outside the control by design, and reading as not-exceeded is correct.
//     Refusing them would deny the product to its own users on a bound written
//     for agents.
//   - METERED BUT UNANSWERABLE — an agent whose counter cannot be read. This is
//     the fail-closed branch: the meter does not know whether the threshold has
//     been passed, and a control that cannot answer must not answer "no".
func (m *Meter) Read(ctx context.Context) Reading {
	ws, agent, metered := m.meteredAgent(ctx)
	if !metered {
		return Reading{Limit: m.limit}
	}
	if m.rdb == nil {
		return Reading{Limit: m.limit, Exceeded: true}
	}
	bucket := m.bucket()
	observed, err := m.counter(ctx, m.countKey(ws, agent, bucket))
	if err != nil {
		return Reading{Limit: m.limit, Exceeded: true}
	}
	granted, err := m.counter(ctx, m.grantKey(ws, agent, bucket))
	if err != nil {
		return Reading{Observed: observed, Limit: m.limit, Exceeded: true}
	}
	limit := m.limit + granted
	return Reading{Observed: observed, Limit: limit, Exceeded: observed >= limit}
}

// counter reads one window key, treating a missing key (redis.Nil) as zero —
// nothing charged this window yet — and surfacing every other error so the
// caller fails closed.
func (m *Meter) counter(ctx context.Context, key string) (int, error) {
	v, err := m.rdb.Get(ctx, key).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	return v, nil
}

// ttlSeconds covers the window plus an hour of clock-skew slack, so a counter
// never outlives its window (over-counting a later one) nor expires inside it
// (under-counting this one).
func (m *Meter) ttlSeconds() int {
	return int((m.window + time.Hour).Seconds())
}
