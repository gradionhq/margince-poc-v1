// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// recordingClaims is the claim store as the registry uses it, with the verdict
// scripted per test and every call recorded.
type recordingClaims struct {
	verdict    Claim
	claimErr   error
	settleErr  error
	failErr    error
	releaseErr error

	claimed  []string // "tool/key/digest" per Claim
	settled  []string // "tool/key" per Settle
	failed   []string // "tool/key: reason" per Fail
	released []string // "tool/key" per Release
	stored   json.RawMessage
	// storedRecords is what Settle was told the answer cost, which is what a
	// replay of it must charge.
	storedRecords int
	// live is closed-over state proving the bookkeeping call outlived the
	// caller's cancellation.
	settleCtxLive bool
	failCtxLive   bool
}

func (c *recordingClaims) Claim(_ context.Context, tool, key, digest string) (Claim, error) {
	c.claimed = append(c.claimed, tool+"/"+key+"/"+digest)
	if c.claimErr != nil {
		return Claim{}, c.claimErr
	}
	return c.verdict, nil
}

func (c *recordingClaims) Settle(ctx context.Context, tool, key string, result json.RawMessage, records int) error {
	c.settled = append(c.settled, tool+"/"+key)
	c.stored, c.storedRecords = result, records
	c.settleCtxLive = ctx.Err() == nil
	return c.settleErr
}

func (c *recordingClaims) Fail(ctx context.Context, tool, key, reason string) error {
	c.failed = append(c.failed, tool+"/"+key+": "+reason)
	c.failCtxLive = ctx.Err() == nil
	return c.failErr
}

func (c *recordingClaims) Release(_ context.Context, tool, key string) error {
	c.released = append(c.released, tool+"/"+key)
	return c.releaseErr
}

// answeringReader is the replay probe's view of the world: which records the
// caller may still read, and how many times it was asked.
type answeringReader struct {
	deny map[ids.UUID]error
	err  error
	// reads counts every probe, so a test can prove the whole evidence list was
	// walked rather than the first entry.
	reads int
}

func (a *answeringReader) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	a.reads++
	if a.err != nil {
		return datasource.Record{}, a.err
	}
	if err, denied := a.deny[ref.ID]; denied {
		return datasource.Record{}, err
	}
	return datasource.Record{Ref: ref}, nil
}

// writingTool is a mutation: it records that it ran, and answers with the
// records it served through the one place a record becomes tool output.
type writingTool struct {
	spec    mcp.ToolSpec
	runs    int
	records []ids.UUID
	fail    error
	// onHandle runs inside the handler, for the tests that need something to
	// happen while the call is in flight (the caller hanging up).
	onHandle func()
}

func (w *writingTool) Spec() mcp.ToolSpec { return w.spec }

func (w *writingTool) Handle(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	w.runs++
	if w.onHandle != nil {
		w.onHandle()
	}
	for _, id := range w.records {
		newWireRecord(ctx, datasource.Record{Ref: datasource.EntityRef{Type: datasource.EntityDeal, ID: id}})
	}
	if w.fail != nil {
		return nil, w.fail
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func writeToolSpec(name string) mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: name, Title: name, Version: testToolVersion, Description: describedForRegistration,
		InputSchema:   json.RawMessage(`{"type":"object","properties":{"note":{"type":"string"}}}`),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
	}
}

// retryFixture is the one setup these tests share: a mutating tool on a
// registry with a scripted claim store, a reader, and a counting charger.
type retryFixture struct {
	registry *Registry
	tool     *writingTool
	claims   *recordingClaims
	reader   *answeringReader
	charger  *countingCharger
	ctx      context.Context
}

func newRetryFixture(t *testing.T) *retryFixture {
	t.Helper()
	f := &retryFixture{
		tool:    &writingTool{spec: writeToolSpec("send_email")},
		claims:  &recordingClaims{},
		reader:  &answeringReader{},
		charger: newCountingCharger(),
	}
	f.registry = NewRegistry(nil, auth.NewGate(fullSeatAuthority{}),
		WithIdempotency(f.claims), WithReplayReader(f.reader), WithQuotaCharger(f.charger))
	f.registry.Register(f.tool)
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	f.ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(principal.ScopeWrite),
	})
	return f
}

func (f *retryFixture) invoke(t *testing.T, args string) (json.RawMessage, error) {
	t.Helper()
	return f.registry.Invoke(f.ctx, f.tool.spec.Name, json.RawMessage(args))
}

func TestAFreshClaimRunsTheToolAndRecordsItsResult(t *testing.T) {
	f := newRetryFixture(t)
	f.claims.verdict = Claim{State: ClaimFresh}

	out, err := f.invoke(t, `{"idempotency_key":"k-1","note":"hi"}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if f.tool.runs != 1 {
		t.Fatalf("the tool ran %d times, want 1", f.tool.runs)
	}
	if len(f.claims.settled) != 1 || f.claims.settled[0] != "send_email/k-1" {
		t.Fatalf("settled = %v", f.claims.settled)
	}
	if len(f.claims.released) != 0 {
		t.Fatalf("a successful call released its claim: %v", f.claims.released)
	}
	// What is recorded is what the caller received — the sealed envelope, not
	// the handler's bare payload. A replay that answered the payload would drop
	// the trust tier, the freshness and the evidence the replay gate itself
	// then needs.
	if string(f.claims.stored) != string(out) {
		t.Fatalf("recorded %s, answered %s", f.claims.stored, out)
	}
	if !strings.Contains(string(out), `"schema_version"`) {
		t.Fatalf("the recorded result is not an envelope: %s", out)
	}
	// The claim is taken over the CALL, so the digest is the same hash an
	// approval would bind to.
	res, err := splitReserved(json.RawMessage(`{"note":"hi"}`))
	if err != nil {
		t.Fatalf("hash the bare call: %v", err)
	}
	if want := "send_email/k-1/" + res.DiffHash; f.claims.claimed[0] != want {
		t.Fatalf("claimed %q, want %q", f.claims.claimed[0], want)
	}
}

func TestACallWithNoKeyNeverTouchesTheClaimStore(t *testing.T) {
	f := newRetryFixture(t)
	if _, err := f.invoke(t, `{"note":"hi"}`); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(f.claims.claimed) != 0 {
		t.Fatalf("an unkeyed call claimed %v", f.claims.claimed)
	}
	if f.tool.runs != 1 {
		t.Fatalf("the tool ran %d times, want 1", f.tool.runs)
	}
}

func TestAReplayAnswersTheFirstResultWithoutRunningTheToolAgain(t *testing.T) {
	f := newRetryFixture(t)
	recorded := ids.NewV7()
	stored := json.RawMessage(fmt.Sprintf(
		`{"schema_version":"1.0.0","evidence":[{"record_type":"deal","record_id":"%s"}],"data":{"ok":true}}`, recorded))
	f.claims.verdict = Claim{State: ClaimReplay, Result: stored, Records: 1}

	out, err := f.invoke(t, `{"idempotency_key":"k-1","note":"hi"}`)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if f.tool.runs != 0 {
		t.Fatal("the tool ran on a replay — the effect happened twice")
	}
	if string(out) != string(stored) {
		t.Fatalf("a replay altered the recorded answer:\n got %s\nwant %s", out, stored)
	}
	if f.reader.reads != 1 {
		t.Fatalf("the replay probed %d records, want 1", f.reader.reads)
	}
	if f.charger.reads() != 1 {
		t.Fatalf("the replay charged %d records against the read bound, want 1", f.charger.reads())
	}
}

// A recorded result is a receipt that outlives the authority it was produced
// under. Revocation binds mid-session, and it has to bind to the retry too.
func TestAReplayIsRefusedWhenTheCallerCanNoLongerSeeWhatItCarries(t *testing.T) {
	for _, tc := range []struct {
		name string
		deny error
	}{
		{name: "the row is gone or out of scope", deny: apperrors.ErrNotFound},
		{name: "the object grant was pulled", deny: apperrors.ErrPermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetryFixture(t)
			visible, hidden := ids.NewV7(), ids.NewV7()
			f.reader.deny = map[ids.UUID]error{hidden: tc.deny}
			f.claims.verdict = Claim{State: ClaimReplay, Result: json.RawMessage(fmt.Sprintf(
				`{"schema_version":"1.0.0","evidence":[{"record_type":"deal","record_id":"%s"},`+
					`{"record_type":"deal","record_id":"%s"}],"data":{"ok":true}}`, visible, hidden))}

			out, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
			// Existence-hiding: the caller learns the same thing whichever gate
			// turned them away, and nothing about what they could see yesterday.
			if !errors.Is(err, apperrors.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			if out != nil {
				t.Fatalf("part of the recorded document was served anyway: %s", out)
			}
			if f.charger.reads() != 0 {
				t.Fatalf("a refused replay charged %d records", f.charger.reads())
			}
		})
	}
}

// A reader failure that is neither of the two visibility answers is somebody
// else's problem — it travels rather than being flattened into "not found",
// which would tell the caller their access changed when the database was
// merely unreachable.
func TestAReplayForwardsAReadFailureThatIsNotAVisibilityAnswer(t *testing.T) {
	f := newRetryFixture(t)
	f.reader.err = errors.New("the pool is exhausted")
	f.claims.verdict = Claim{State: ClaimReplay, Result: json.RawMessage(fmt.Sprintf(
		`{"schema_version":"1.0.0","evidence":[{"record_type":"deal","record_id":"%s"}]}`, ids.NewV7()))}

	if _, err := f.invoke(t, `{"idempotency_key":"k-1"}`); err == nil || errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want the reader's own failure", err)
	}
}

// Admission checked scope, tier and seat; object RBAC and row scope live inside
// the handler, which a replay never enters. So the only authority a replay can
// re-check is the one attached to a record it can NAME — and a document citing
// none is unprovable rather than harmless.
func TestAReplayThatNamesNoRecordIsRefused(t *testing.T) {
	f := newRetryFixture(t)
	stored := json.RawMessage(`{"schema_version":"1.0.0","evidence":[],"data":{"rows":["derived"]}}`)
	f.claims.verdict = Claim{State: ClaimReplay, Result: stored, Records: 0}

	out, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if out != nil {
		t.Fatalf("an unprovable document was served: %s", out)
	}
}

// A surface that cannot prove the caller may still see the records must not pay
// out — a missing dependency is the composition root's defect, and serving on
// the strength of it is what the probe exists to prevent.
func TestAReplayIsRefusedWhenNoReaderIsWired(t *testing.T) {
	f := newRetryFixture(t)
	f.registry = NewRegistry(nil, auth.NewGate(fullSeatAuthority{}),
		WithIdempotency(f.claims), WithQuotaCharger(f.charger))
	f.registry.Register(f.tool)
	f.claims.verdict = Claim{State: ClaimReplay, Result: json.RawMessage(fmt.Sprintf(
		`{"schema_version":"1.0.0","evidence":[{"record_type":"deal","record_id":"%s"}]}`, ids.NewV7()))}

	if _, err := f.invoke(t, `{"idempotency_key":"k-1"}`); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAReplayIsRefusedWhenTheRecordedBytesAreNotAnEnvelope(t *testing.T) {
	for _, stored := range []string{
		`not json at all`,
		`{"evidence":[{"record_type":"","record_id":"00000000-0000-0000-0000-000000000000"}]}`,
		`{"evidence":[{"record_type":"deal","record_id":"00000000-0000-0000-0000-000000000000"}]}`,
	} {
		f := newRetryFixture(t)
		f.claims.verdict = Claim{State: ClaimReplay, Result: json.RawMessage(stored)}
		out, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("%s → err = %v, want ErrNotFound", stored, err)
		}
		if out != nil {
			t.Fatalf("%s was served anyway", stored)
		}
	}
}

// A replay this surface cannot count is a replay it does not serve. Unlike a
// fresh write — whose effect has already happened, so refusing it would report
// a completed act as a failure — a withheld replay costs the caller a document
// they can ask for again.
func TestAReplayThatCannotBeChargedIsWithheld(t *testing.T) {
	f := newRetryFixture(t)
	f.charger.err = errors.New("the meter is unreachable")
	f.claims.verdict = Claim{State: ClaimReplay, Records: 1, Result: json.RawMessage(fmt.Sprintf(
		`{"schema_version":"1.0.0","evidence":[{"record_type":"deal","record_id":"%s"}]}`, ids.NewV7()))}

	out, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if out != nil {
		t.Fatalf("the uncountable replay was served: %s", out)
	}
}

func TestAHeldKeyRefusesRatherThanActingTwice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state ClaimState
		says  string
	}{
		{name: "the first attempt has not finished", state: ClaimInFlight, says: "has not finished"},
		{name: "the key is held against different arguments", state: ClaimMismatch, says: "DIFFERENT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetryFixture(t)
			f.claims.verdict = Claim{State: tc.state}

			_, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
			if !errors.Is(err, apperrors.ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say which conflict it is: %v", err)
			}
			// The message has to say what to do next, not only what went wrong.
			if !strings.Contains(err.Error(), "send_email") {
				t.Errorf("the refusal does not name the tool: %v", err)
			}
			if f.tool.runs != 0 {
				t.Fatal("the tool ran despite the key being held")
			}
		})
	}
}

func TestAnUnknownClaimStateIsRefusedRatherThanRun(t *testing.T) {
	f := newRetryFixture(t)
	f.claims.verdict = Claim{State: ClaimState(99)}
	if _, err := f.invoke(t, `{"idempotency_key":"k-1"}`); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if f.tool.runs != 0 {
		t.Fatal("a state nothing understood was resolved into running the tool")
	}
}

// At-most-once is the whole promise, so a surface that cannot keep it says so.
// Running anyway — the REST middleware's posture, which is right for a header a
// client may not have meant — would make a promise the retry then discovers was
// never kept, and the act it was asked about is irreversible.
func TestAClaimStoreFailureRefusesTheCallRatherThanRunningItUnprotected(t *testing.T) {
	f := newRetryFixture(t)
	f.claims.claimErr = errors.New("the claim transaction failed")

	_, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if f.tool.runs != 0 {
		t.Fatal("the tool ran under a key the surface could not claim")
	}
	// The caller is told to retry, because nothing happened.
	if !strings.Contains(err.Error(), "was not run") {
		t.Errorf("the refusal does not say the call did not happen: %v", err)
	}
	// And an UNKEYED call is untouched by a broken claim store.
	if _, err := f.invoke(t, `{}`); err != nil {
		t.Fatalf("an unkeyed call was refused: %v", err)
	}
}

// A handler can fail AFTER its write committed — create_record commits the row
// and then reads it back — so "the call returned an error" is not "nothing
// happened", and giving the key back there is how one key creates two records.
func TestAFailedRunKeepsItsKeyRatherThanInvitingASecondAttempt(t *testing.T) {
	f := newRetryFixture(t)
	f.claims.verdict = Claim{State: ClaimFresh}
	f.tool.fail = fmt.Errorf("the provider refused the write: %w", apperrors.ErrConflict)

	if _, err := f.invoke(t, `{"idempotency_key":"k-1"}`); err == nil {
		t.Fatal("the handler's failure was swallowed")
	}
	if len(f.claims.released) != 0 {
		t.Fatalf("a run that may have taken effect gave its key back: %v", f.claims.released)
	}
	if len(f.claims.settled) != 0 {
		t.Fatalf("a failure was recorded as a replayable result: %v", f.claims.settled)
	}
	if len(f.claims.failed) != 1 {
		t.Fatalf("failed = %v, want the run recorded", f.claims.failed)
	}
	// The recorded reason is the SENTINEL's words, not the refusal's own prose:
	// it is stored for the window and handed to whoever presents the key next.
	if got := f.claims.failed[0]; got != "send_email/k-1: it conflicted with another change" {
		t.Fatalf("recorded %q — a stored reason must not carry the refusal's own text", got)
	}
}

// What the caller is told when they present that key again. Not "in flight"
// (the attempt is over) and not a fresh run (it may have taken effect) — the
// one thing they can act on.
func TestARetryOfAFailedRunIsToldToCheckBeforeUsingANewKey(t *testing.T) {
	f := newRetryFixture(t)
	f.claims.verdict = Claim{State: ClaimFailed, Reason: "it conflicted with another change"}

	_, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	for _, want := range []string{"may or", "NEW key", "it conflicted with another change"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if f.tool.runs != 0 {
		t.Fatal("the tool ran again under a key whose earlier attempt may have taken effect")
	}
}

// Both bookkeeping failures are logged and swallowed, and that is the point:
// by the time either runs the effect has committed, and reporting a completed
// act as a failure is worse than an unreplayable key — the caller would retry
// what already happened.
func TestBookkeepingFailuresNeverChangeWhatTheCallerIsTold(t *testing.T) {
	t.Run("settling fails after a successful call", func(t *testing.T) {
		f := newRetryFixture(t)
		f.claims.verdict = Claim{State: ClaimFresh}
		f.claims.settleErr = errors.New("the update failed")
		out, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
		if err != nil {
			t.Fatalf("a completed call was reported as failed: %v", err)
		}
		if !strings.Contains(string(out), `"ok":true`) {
			t.Fatalf("out = %s", out)
		}
	})
	t.Run("recording a failed run fails", func(t *testing.T) {
		f := newRetryFixture(t)
		f.claims.verdict = Claim{State: ClaimFresh}
		f.tool.fail = errors.New("the provider refused the write")
		f.claims.failErr = errors.New("the update failed")
		_, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
		if err == nil || !strings.Contains(err.Error(), "provider refused") {
			t.Fatalf("err = %v, want the handler's own failure", err)
		}
	})
	t.Run("releasing an unrun key fails", func(t *testing.T) {
		f, approvals := approvedRetryFixture(t)
		approvals.redeemErr = fmt.Errorf("not yours: %w", apperrors.ErrApprovalTokenInvalid)
		f.claims.releaseErr = errors.New("the delete failed")
		f.claims.verdict = Claim{State: ClaimFresh}
		_, err := f.invoke(t, `{"idempotency_key":"k-1","approval_id":"`+ids.NewV7().String()+`"}`)
		if !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
			t.Fatalf("err = %v, want the redemption's own refusal", err)
		}
	})
}

// Ignoring the key would promise retry safety the surface cannot keep, and the
// caller would never learn otherwise — what it fails to prevent is a second
// irreversible act.
func TestAKeyIsRefusedOnASurfaceThatCannotClaimIt(t *testing.T) {
	f := newRetryFixture(t)
	f.registry = NewRegistry(nil, auth.NewGate(fullSeatAuthority{}), WithQuotaCharger(f.charger))
	f.registry.Register(f.tool)

	_, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want a BadArgsError", err)
	}
	if f.tool.runs != 0 {
		t.Fatal("the call ran unprotected under a key the surface cannot claim")
	}
	// And an unkeyed call on the same surface is unaffected.
	if _, err := f.invoke(t, `{}`); err != nil {
		t.Fatalf("an unkeyed call was refused too: %v", err)
	}
}

// The claim is taken AFTER admission, so a caller the gate turns away cannot
// occupy a key — theirs, or anyone else's under the same passport.
func TestARefusedCallerNeverOccupiesAKey(t *testing.T) {
	f := newRetryFixture(t)
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(principal.ScopeRead), // not write
	})
	if _, err := f.registry.Invoke(ctx, "send_email", json.RawMessage(`{"idempotency_key":"k-1"}`)); err == nil {
		t.Fatal("a call outside the passport's scope was admitted")
	}
	if len(f.claims.claimed) != 0 {
		t.Fatalf("a refused caller claimed %v", f.claims.claimed)
	}
}

// consumingApprovals redeems an approval EXACTLY once, like the real engine:
// a second redemption of the same id fails, because the row is the authority
// object and it is spent.
type consumingApprovals struct {
	redeemed  map[ids.ApprovalID]bool
	redeems   int
	stageErr  error
	staged    ids.ApprovalID
	redeemErr error
}

func (a *consumingApprovals) Stage(context.Context, StageRequest) (ids.ApprovalID, error) {
	return a.staged, a.stageErr
}

// StageQuotaRelease is the §2.4 step-up, which none of these scenarios reaches:
// they are about the approval a 🟡 call already carries, not about a quota
// ceiling. It answers "nothing staged" so a test that DID reach it would fail
// on the missing step-up rather than pass on a fabricated one.
func (a *consumingApprovals) StageQuotaRelease(context.Context, QuotaReleaseRequest) (ids.ApprovalID, bool, error) {
	return ids.ApprovalID{}, false, nil
}

func (a *consumingApprovals) Redeem(_ context.Context, id ids.ApprovalID, _, _ string) (int64, bool, error) {
	a.redeems++
	if a.redeemErr != nil {
		return 0, false, a.redeemErr
	}
	if a.redeemed[id] {
		return 0, false, fmt.Errorf("approval already consumed: %w", apperrors.ErrApprovalTokenInvalid)
	}
	if a.redeemed == nil {
		a.redeemed = map[ids.ApprovalID]bool{}
	}
	a.redeemed[id] = true
	return 0, false, nil
}

// approvedRetryFixture is a 🟡 tool on a surface with a single-use approvals
// engine — the shape the retry key is worth most on, since a confirm-first
// tool is a confirm-first tool because its effect cannot be taken back.
func approvedRetryFixture(t *testing.T) (*retryFixture, *consumingApprovals) {
	t.Helper()
	f := &retryFixture{
		tool:    &writingTool{spec: writeToolSpec("send_email")},
		claims:  &recordingClaims{},
		reader:  &answeringReader{},
		charger: newCountingCharger(),
	}
	f.tool.spec.Tier = mcp.TierConfirmationRequired
	// It answers with the record it changed, as every mutating tool on this
	// surface does — which is what makes its result replayable at all.
	f.tool.records = []ids.UUID{ids.NewV7()}
	approvals := &consumingApprovals{staged: ids.From[ids.ApprovalKind](ids.NewV7())}
	f.registry = NewRegistry(approvals, auth.NewGate(fullSeatAuthority{}),
		WithIdempotency(f.claims), WithReplayReader(f.reader), WithQuotaCharger(f.charger))
	f.registry.Register(f.tool)
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	f.ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(principal.ScopeWrite),
	})
	return f, approvals
}

// The claim is taken BEFORE the approval is redeemed, and this is why. An
// approval is single-use, so redeeming first means the retry of an approved
// send — the exact call whose response was lost — dies on the consumed
// approval and never reaches the result the first attempt recorded. Retry
// safety would be missing from precisely the tools that need it.
func TestTheRetryOfAnApprovedCallReplaysInsteadOfRedeemingTwice(t *testing.T) {
	f, approvals := approvedRetryFixture(t)
	approval := ids.NewV7()
	call := `{"idempotency_key":"k-1","approval_id":"` + approval.String() + `"}`

	f.claims.verdict = Claim{State: ClaimFresh}
	first, err := f.invoke(t, call)
	if err != nil {
		t.Fatalf("the approved call: %v", err)
	}
	if f.tool.runs != 1 || approvals.redeems != 1 {
		t.Fatalf("runs=%d redeems=%d, want 1 and 1", f.tool.runs, approvals.redeems)
	}

	// The response was lost; the agent repeats the identical call.
	f.claims.verdict = Claim{State: ClaimReplay, Result: first, Records: 1}
	second, err := f.invoke(t, call)
	if err != nil {
		t.Fatalf("the retry of the approved call: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("the retry answered differently:\nfirst  %s\nsecond %s", first, second)
	}
	if f.tool.runs != 1 {
		t.Fatal("the retry sent a second email")
	}
	if approvals.redeems != 1 {
		t.Fatalf("the retry attempted redemption %d times — a spent approval must not be asked for again", approvals.redeems)
	}
}

// The mirror of the ordering above: redemption comes AFTER the claim, so a
// refused redemption gives the key straight back. Nothing ran, so nothing is
// in doubt.
func TestARefusedRedemptionGivesTheKeyBack(t *testing.T) {
	f, approvals := approvedRetryFixture(t)
	approvals.redeemErr = fmt.Errorf("that approval is not yours: %w", apperrors.ErrApprovalTokenInvalid)
	f.claims.verdict = Claim{State: ClaimFresh}

	_, err := f.invoke(t, `{"idempotency_key":"k-1","approval_id":"`+ids.NewV7().String()+`"}`)
	if !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("err = %v, want the redemption's own refusal", err)
	}
	if f.tool.runs != 0 {
		t.Fatal("the tool ran on a refused redemption")
	}
	if len(f.claims.released) != 1 {
		t.Fatalf("released = %v, want the key back — nothing ran under it", f.claims.released)
	}
	if len(f.claims.failed) != 0 {
		t.Fatalf("an unrun call was recorded as a failed run: %v", f.claims.failed)
	}
}

// A 🟡 call with no approval is STAGED, and staging must not hold a key: the
// call did not run, and the retry that redeems the approval is the same call
// under the same key.
func TestAStagedCallHoldsNoKey(t *testing.T) {
	f, _ := approvedRetryFixture(t)
	if _, err := f.invoke(t, `{"idempotency_key":"k-1"}`); err == nil {
		t.Fatal("a 🟡 call with no approval was executed")
	}
	if len(f.claims.claimed) != 0 {
		t.Fatalf("staging claimed %v", f.claims.claimed)
	}
}

// The transport dropping is the motivating case, and it cancels the request
// context — so bookkeeping done on that context would fail exactly when the
// caller most needs the result recorded, leaving the claim in flight for the
// whole window and the one answer the retry exists to fetch unreachable.
func TestTheRunIsRecordedEvenWhenTheCallerIsGone(t *testing.T) {
	t.Run("a completed run", func(t *testing.T) {
		f := newRetryFixture(t)
		f.claims.verdict = Claim{State: ClaimFresh}
		ctx, cancel := context.WithCancel(f.ctx)
		f.ctx = ctx
		f.tool.onHandle = cancel // the client hangs up mid-call

		if _, err := f.invoke(t, `{"idempotency_key":"k-1"}`); err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if len(f.claims.settled) != 1 {
			t.Fatalf("settled = %v, want the result recorded", f.claims.settled)
		}
		if !f.claims.settleCtxLive {
			t.Fatal("settlement ran on the caller's canceled context, so it could only ever fail")
		}
	})
	t.Run("a failed run", func(t *testing.T) {
		f := newRetryFixture(t)
		f.claims.verdict = Claim{State: ClaimFresh}
		ctx, cancel := context.WithCancel(f.ctx)
		f.ctx = ctx
		f.tool.onHandle = cancel
		f.tool.fail = errors.New("the provider refused the write")

		if _, err := f.invoke(t, `{"idempotency_key":"k-1"}`); err == nil {
			t.Fatal("the failure was swallowed")
		}
		if !f.claims.failCtxLive {
			t.Fatal("the failure was recorded on the caller's canceled context")
		}
	})
}

// A read has nothing to make safe to repeat, and its schema says so — so
// accepting the argument anyway would be the surface contradicting the schema
// it advertises, which is the defect A4 exists to close.
func TestAReadOnlyToolRefusesTheRetryKeyItNeverAdvertised(t *testing.T) {
	f := newRetryFixture(t)
	read := &writingTool{spec: readToolSpec("search_records")}
	f.registry.Register(read)
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
	})

	_, err := f.registry.Invoke(ctx, "search_records", json.RawMessage(`{"idempotency_key":"k-1"}`))
	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want a BadArgsError", err)
	}
	if read.runs != 0 {
		t.Fatal("a read answer was frozen for the replay window")
	}
	if len(f.claims.claimed) != 0 {
		t.Fatalf("a read tool claimed %v", f.claims.claimed)
	}
	// The same tool without the key is untouched.
	if _, err := f.registry.Invoke(ctx, "search_records", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("an unkeyed read was refused: %v", err)
	}
}

// A replay costs what the CALL cost, recorded with the result — not the length
// of the evidence list, which dedupes and so is the smaller number every time
// they differ. Deriving the charge from the document would make retrying an
// answer cheaper than asking for it.
func TestAReplayCostsWhatTheCallCostAndNotWhatItCanName(t *testing.T) {
	f := newRetryFixture(t)
	same := ids.NewV7()
	f.tool.records = []ids.UUID{same, same, ids.NewV7()} // one record served twice
	f.claims.verdict = Claim{State: ClaimFresh}

	out, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if f.charger.reads() != 3 {
		t.Fatalf("the call charged %d, want 3 records served", f.charger.reads())
	}
	if f.claims.storedRecords != 3 {
		t.Fatalf("recorded cost %d, want the 3 the call was charged", f.claims.storedRecords)
	}

	replayed := newRetryFixture(t)
	replayed.claims.verdict = Claim{State: ClaimReplay, Result: out, Records: f.claims.storedRecords}
	if _, err := replayed.invoke(t, `{"idempotency_key":"k-1"}`); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.charger.reads() != 3 {
		t.Fatalf("the replay charged %d, want the 3 the call cost — its evidence list names only 2",
			replayed.charger.reads())
	}
}
