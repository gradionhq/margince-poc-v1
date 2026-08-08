// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentquota

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// keyPrefix is the namespace every counter shares.
//
// It still spells the read meter this package grew out of, and that is
// deliberate. Renaming it would orphan every live `reads` window at the moment
// of deploy, and an orphaned counter reads as ZERO — handing every connected
// Passport one fresh full allowance on the exact release that is supposed to
// tighten the bound, with nothing in the gate able to see it happen. The
// spelling is history; the counter is a security control.
const keyPrefix = "msr:"

// CostCeiling answers how many model tokens ONE Passport may spend inside a
// window — the workspace's own AI budget divided by the credentials sharing it
// (MCP-SESS-COST: "tenant budget ÷ active sessions").
//
// It is a seam rather than a number because both halves of that division live
// outside this package: the budget belongs to the AI runtime and the divisor to
// identity. A meter without one does not bound cost at all, which is a real
// composition — a role that serves no agent principals has no share to compute.
type CostCeiling interface {
	TokensPerPassport(ctx context.Context) int
}

// Meter counts what one Passport has spent inside a window. The zero value is
// not usable; construct with New, NewWithClock or Unmetered.
type Meter struct {
	rdb    *redis.Client
	limits Limits
	window time.Duration
	now    func() time.Time
	cost   CostCeiling
	// unbounded marks the meter Unmetered built: it admits everything and
	// records nothing, because this composition declared no bound rather than
	// failed to reach one.
	unbounded bool
}

// New constructs a Meter over rdb using the real wall clock. A nil rdb is
// allowed and makes the meter fail-closed — every governed counter reports its
// threshold passed, because a counter that cannot be read cannot show headroom.
func New(rdb *redis.Client, limits Limits, window time.Duration) *Meter {
	return NewWithClock(rdb, limits, window, time.Now)
}

// NewWithClock takes the clock as a dependency so the fixed window a charge
// lands in is asserted by advancing time rather than by sleeping against the
// real one (T11): the clock's reading picks both the Redis key and the expiry,
// deterministically.
func NewWithClock(rdb *redis.Client, limits Limits, window time.Duration, now func() time.Time) *Meter {
	if window <= 0 {
		window = DefaultWindow
	}
	return &Meter{rdb: rdb, limits: limits.withDefaults(), window: window, now: now}
}

// Unmetered is a meter that counts nothing and bounds nothing — for a
// composition that DECLARES it serves no bounded agent surface.
//
// It is not the same thing as a meter that cannot reach its counter, and the
// difference is the whole of why it is a named constructor rather than a nil. A
// meter with no Redis fails CLOSED, because it was asked to bound an agent and
// could not answer. This one was never asked: the test harness and any role that
// serves no agent principals compose it on purpose, and a reader who finds it
// knows a decision was taken rather than a dependency forgotten.
//
// Nothing in a production api path may use it. cmd/api wires the Redis-backed
// meter, and a deployment that misconfigures Redis gets the loud refusal.
func Unmetered() *Meter {
	return &Meter{limits: Limits{}.withDefaults(), window: DefaultWindow, now: time.Now, unbounded: true}
}

// WithCostCeiling installs the share one Passport may spend of the workspace AI
// budget. Set at assembly, before any request is served, for the same reason
// RebindFrom is: the ceiling is a dependency of the composition, never of a call.
func (m *Meter) WithCostCeiling(c CostCeiling) *Meter {
	m.cost = c
	return m
}

// RebindFrom copies src's client and configuration onto this meter — the
// boot-time injection point compose uses WITHOUT itself naming a Redis client
// (that dependency stays in the cmd/platform tiers). newServer constructs a
// fail-closed meter and shares that ONE pointer with every charge point; a
// WithAgentQuota option rebinds it from the live meter once the Redis client and
// the deployment config are known, so every holder sees the live meter without
// re-plumbing. Called at server assembly, before any request is served, so it
// never races a charge — the same discipline overlaybudget.Meter.RebindFrom
// follows.
func (m *Meter) RebindFrom(src *Meter) {
	// EVERY field, including a nil ceiling. A conditional copy would leave a
	// rebound meter judging cost against the composition it used to be in,
	// which is silent: the counter refuses nothing, so a wrong ceiling shows up
	// only as a warning that fires at the wrong volume.
	m.rdb, m.limits, m.window = src.rdb, src.limits, src.window
	m.now, m.unbounded, m.cost = src.now, src.unbounded, src.cost
}

// Limit is the configured threshold for one counter, for a refusal envelope to
// report without a second reading of the window.
func (m *Meter) Limit(c Counter) int { return m.limits.of(c) }

// Window is the span one counter covers. A caller computing anything PER window
// — the cost ceiling's share of a monthly budget is the one today — must
// pro-rate against this rather than against the default, or it compares a share
// of one span with a count over another.
func (m *Meter) Window() time.Duration { return m.window }

// Reading is what the meter knows about one Passport's window for one counter.
type Reading struct {
	// Counter is which quota this reading is of, so a refusal can name it
	// rather than describing it.
	Counter Counter
	// Observed is what has been charged in this window so far.
	Observed int
	// Limit is the EFFECTIVE threshold Observed is judged against — the
	// configured one plus whatever a human has released this window.
	Limit int
	// Allowance is what ONE release adds, which is the configured threshold and
	// NOT the effective one. They differ the moment a window has been released
	// once, and the approval screen quotes this: telling a human "continue for
	// another 200" while approving adds 100 is a number that is wrong in the
	// direction that matters.
	Allowance int
	// Exceeded reports that the next call must be refused. It is a field
	// rather than a comparison the caller makes, so the fail-closed answer
	// cannot be reconstructed wrongly by a second caller.
	Exceeded bool
	// Bucket names the window this reading is of. A release has to land in the
	// window the human was SHOWN — carrying the bucket is what stops an
	// approval answered an hour later from widening a window nobody looked at.
	Bucket int64
}

// countKey is one agent's counter for one window bucket. The bucket comes from
// the injected clock, never Redis TIME, so rollover is deterministic under test.
func (m *Meter) countKey(ws ids.UUID, agent string, c Counter, bucket int64) string {
	return fmt.Sprintf(keyPrefix+"%s:%s:%s:%d", ws.String(), agent, c, bucket)
}

// releaseKey counts the allowances a human has granted this counter this window.
// It is a SEPARATE key from the count rather than a decrement of it, because the
// two are different facts: how much an agent has done, and how much a human has
// agreed it may do. Folding a release into the counter would erase the first,
// and the observed volume is the number the next approval screen has to show.
func (m *Meter) releaseKey(ws ids.UUID, agent string, c Counter, bucket int64) string {
	return m.countKey(ws, agent, c, bucket) + ":released"
}

// addScript adds n to the window counter and returns the new total, setting the
// fixed-window expiry on FIRST write only — so the window is fixed rather than
// sliding, and a busy agent's counter does not renew itself into never
// resetting. ARGV=[n, ttlSeconds].
var addScript = redis.NewScript(`
local n = tonumber(ARGV[1])
local total = redis.call('INCRBY', KEYS[1], n)
if total == n then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[2])) end
return total`)

// governed reports whether this caller is inside the control at all.
//
// ONLY an agent is. A human's authority is RBAC at the store and they answered
// for the action themselves; these bounds exist to make an AGENT's volume
// visible to the human who granted it. That is the same line auth.Gate.Admit
// draws, drawn again here so the meter is safe to call from anywhere rather than
// only from behind the gate.
func governed(ctx context.Context) bool {
	actor, present := principal.Actor(ctx)
	return present && actor.Type == principal.PrincipalAgent
}

// callerKey names the (workspace, Passport) pair a governed call's counters are
// kept under, and reports whether one could be named at all.
//
// The two "no" answers this package gives are DIFFERENT and must not be folded
// together. `governed` says the caller is outside the control — admit. This says
// the caller is inside it and the meter cannot identify them — refuse. An agent
// with no workspace bound to its context is the second: it is a caller these
// bounds govern whose counter cannot be built, and admitting it would be a free
// pass handed out by a wiring fault.
//
// The key is the Passport, falling back to the principal's own id. Every agent
// principal this product mints carries a Passport
// (identity.AgentIdentity.Principal), so the fallback is not a live path — but
// keying on something ALWAYS present is what stops a future agent principal
// without one from being silently exempt.
func callerKey(ctx context.Context) (ws ids.UUID, agent string, named bool) {
	actor, present := principal.Actor(ctx)
	if !present {
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

// usable reports that the meter can actually reach this call's counter.
func (m *Meter) usable(ctx context.Context) (ws ids.UUID, agent string, ok bool) {
	if !governed(ctx) || m.rdb == nil {
		return ws, "", false
	}
	ws, agent, named := callerKey(ctx)
	return ws, agent, named
}

// Bucket is the fixed window a moment falls in. Exported because a release has
// to name the window it was granted for, and the granting path runs as the
// HUMAN — outside this meter's per-call context entirely.
//
// It divides in NANOSECONDS rather than whole seconds: a sub-second window
// truncates to zero seconds and the division panics, and a window is a
// caller-supplied duration with nothing stopping it being 500ms.
func (m *Meter) Bucket() int64 {
	return m.now().UTC().UnixNano() / int64(m.window)
}

// Consume records n against ctx's Passport on one counter.
//
// It records UNCONDITIONALLY and never refuses: the spec's mechanism is that
// crossing a threshold refuses the NEXT call, not that it truncates the answer
// in flight (§2.4). What has already happened has already happened; pretending
// otherwise would under-count the exposure the meter exists to measure.
//
// A non-positive n is a no-op — an empty page served nothing. A call with no
// Passport (a human, or the system) is not metered and records nothing.
func (m *Meter) Consume(ctx context.Context, c Counter, n int) error {
	if n <= 0 {
		return nil
	}
	ws, agent, ok := m.usable(ctx)
	if !ok {
		return nil
	}
	key := m.countKey(ws, agent, c, m.Bucket())
	if err := addScript.Run(ctx, m.rdb, []string{key}, n, m.ttlSeconds()).Err(); err != nil {
		return fmt.Errorf("agentquota: recording %d against %s: %w", n, c, err)
	}
	return nil
}

// Read answers what the meter knows about ctx's agent on one counter, before a
// tool does the thing that counter bounds.
//
// The two "no" answers are different and must not be folded together:
//
//   - NOT METERED — a human, the system, a call with no actor. These are outside
//     the control by design, and reading as not-exceeded is correct. Refusing
//     them would deny the product to its own users on a bound written for agents.
//   - METERED BUT UNANSWERABLE — an agent whose counter cannot be read. This is
//     the fail-closed branch: the meter does not know whether the threshold has
//     been passed, and a control that cannot answer must not answer "no".
func (m *Meter) Read(ctx context.Context, c Counter) Reading {
	bucket := m.Bucket()
	if m.unbounded || !governed(ctx) {
		return Reading{Counter: c, Limit: m.limits.of(c), Allowance: m.limits.of(c), Bucket: bucket}
	}
	if c == Cost {
		return m.readCost(ctx, bucket)
	}
	ws, agent, named := callerKey(ctx)
	if !named || m.rdb == nil {
		// Governed, and unidentifiable or uncountable. Both are the fail-closed
		// branch: this caller is inside the control and the meter cannot answer
		// for them.
		return Reading{Counter: c, Limit: m.limits.of(c), Allowance: m.limits.of(c), Exceeded: true, Bucket: bucket}
	}
	observed, released, err := m.observe(ctx, ws, agent, c, bucket)
	if err != nil {
		return Reading{Counter: c, Limit: m.limits.of(c), Allowance: m.limits.of(c), Exceeded: true, Bucket: bucket}
	}
	limit := m.effectiveLimit(c, released)
	return Reading{
		Counter: c, Observed: observed, Limit: limit, Allowance: m.limits.of(c),
		Exceeded: observed >= limit, Bucket: bucket,
	}
}

// readCost answers the soft counter.
//
// It does NOT fail closed, and that is the one deliberate exception in this
// package. Cost refuses nothing — its only effect is a warning on the answer —
// so a fail-closed reading would raise that warning on every call while Redis is
// unreachable, which is the fastest way to teach a reader to ignore it. An
// unreadable soft counter says nothing instead.
func (m *Meter) readCost(ctx context.Context, bucket int64) Reading {
	if m.cost == nil {
		return Reading{Counter: Cost, Bucket: bucket}
	}
	limit := m.cost.TokensPerPassport(ctx)
	ws, agent, named := callerKey(ctx)
	if limit <= 0 || !named || m.rdb == nil {
		return Reading{Counter: Cost, Limit: limit, Bucket: bucket}
	}
	observed, _, err := m.observe(ctx, ws, agent, Cost, bucket)
	if err != nil {
		return Reading{Counter: Cost, Limit: limit, Bucket: bucket}
	}
	return Reading{Counter: Cost, Observed: observed, Limit: limit, Exceeded: observed >= limit, Bucket: bucket}
}

// observe reads the window's charge and its granted allowances in ONE round
// trip. They are read together because they are judged together: two reads could
// straddle a release and produce an answer neither key ever held — an observed
// count from after the release measured against a ceiling from before it, which
// refuses a caller a human has just released.
func (m *Meter) observe(ctx context.Context, ws ids.UUID, agent string, c Counter, bucket int64) (observed, released int, err error) {
	values, err := m.rdb.MGet(ctx, m.countKey(ws, agent, c, bucket), m.releaseKey(ws, agent, c, bucket)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, err
	}
	if len(values) != 2 {
		return 0, 0, fmt.Errorf("agentquota: reading %s returned %d values, want 2", c, len(values))
	}
	observed, err = asCount(values[0])
	if err != nil {
		return 0, 0, err
	}
	released, err = asCount(values[1])
	if err != nil {
		return 0, 0, err
	}
	return observed, released, nil
}

// asCount reads one MGET slot. A missing key is zero — nothing charged this
// window yet. Anything that is neither absent nor an integer string is a counter
// this meter did not write, and it is an ERROR rather than a zero: reading a
// value it cannot parse as "nothing spent" is how a corrupted key becomes an
// unbounded allowance.
//
//craft:ignore naked-any go-redis MGet answers []any, so the slot's type is the client library's and not a choice made here
func asCount(v any) (int, error) {
	if v == nil {
		return 0, nil
	}
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("agentquota: counter value is %T, not a string", v)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		// Atoi, not Sscanf: Sscanf reads "12abc" as 12 and reports no error,
		// which is precisely the "neither absent nor an integer" case above.
		return 0, fmt.Errorf("agentquota: counter value %q is not a number: %w", s, err)
	}
	if n < 0 {
		// This meter only ever INCRBYs by a positive amount, so a negative
		// counter is not a count — and reading one as headroom is the direction
		// that matters: it puts an agent under a bound it has already passed.
		return 0, fmt.Errorf("agentquota: counter value %d is negative; this meter never writes one", n)
	}
	return n, nil
}

// effectiveLimit is the configured threshold plus what a human has released.
//
// One release grants ONE more allowance of the same size, because that is the
// question the approval screen asked: the agent has read its window's worth,
// may it read another. A release that granted an unbounded continuation would
// make the second confirmation the last one anybody is ever asked for.
func (m *Meter) effectiveLimit(c Counter, released int) int {
	base := m.limits.of(c)
	if released <= 0 {
		return base
	}
	return base * (released + 1)
}

// ttlSeconds covers the window plus an hour of clock-skew slack, so a counter
// never outlives its window (over-counting a later one) nor expires inside it
// (under-counting this one). Redis expiry has one-second granularity, so a
// sub-second window still gets a whole second at minimum — the slack already
// guarantees that, and it is named here so the floor is not a surprise.
func (m *Meter) ttlSeconds() int {
	return max(1, int((m.window + time.Hour).Seconds()))
}

// CostShare is the SOFT counter's reading, and the only counter this meter will
// answer through a method of its own.
//
// It exists so the tool surface can say what it has spent without holding
// anything that could answer a governed counter. The split this package and
// platform/auth keep — the gate reads and refuses, the surface charges — would
// be undone by handing the surface a general Read; a surface that could read
// the counters it pays into could decide whether to pay. Cost refuses nothing,
// so answering it discloses no admission decision.
func (m *Meter) CostShare(ctx context.Context) Reading { return m.Read(ctx, Cost) }
