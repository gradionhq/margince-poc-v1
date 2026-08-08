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
	releaseErr error

	claimed  []string // "tool/key/digest" per Claim
	settled  []string // "tool/key" per Settle
	released []string // "tool/key" per Release
	stored   json.RawMessage
}

func (c *recordingClaims) Claim(_ context.Context, tool, key, digest string) (Claim, error) {
	c.claimed = append(c.claimed, tool+"/"+key+"/"+digest)
	if c.claimErr != nil {
		return Claim{}, c.claimErr
	}
	return c.verdict, nil
}

func (c *recordingClaims) Settle(_ context.Context, tool, key string, result json.RawMessage) error {
	c.settled = append(c.settled, tool+"/"+key)
	c.stored = result
	return c.settleErr
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
}

func (w *writingTool) Spec() mcp.ToolSpec { return w.spec }

func (w *writingTool) Handle(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	w.runs++
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
		charger: &countingCharger{},
	}
	f.registry = NewRegistry(nil, auth.NewGate(fullSeatAuthority{}),
		WithIdempotency(f.claims), WithReplayReader(f.reader), WithReadCharger(f.charger))
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
	f.claims.verdict = Claim{State: ClaimReplay, Result: stored}

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
	if f.charger.charged != 1 {
		t.Fatalf("the replay charged %d records against the read bound, want 1", f.charger.charged)
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
			if f.charger.charged != 0 {
				t.Fatalf("a refused replay charged %d records", f.charger.charged)
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

func TestAReplayRestingOnNoRecordNeedsNoProbe(t *testing.T) {
	f := newRetryFixture(t)
	stored := json.RawMessage(`{"schema_version":"1.0.0","evidence":[],"data":{"pipelines":[]}}`)
	f.claims.verdict = Claim{State: ClaimReplay, Result: stored}

	out, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if string(out) != string(stored) {
		t.Fatalf("out = %s", out)
	}
	if f.reader.reads != 0 || f.charger.calls != 0 {
		t.Fatalf("a record-free replay probed %d and charged %d times", f.reader.reads, f.charger.calls)
	}
}

// A surface that cannot prove the caller may still see the records must not pay
// out — a missing dependency is the composition root's defect, and serving on
// the strength of it is what the probe exists to prevent.
func TestAReplayIsRefusedWhenNoReaderIsWired(t *testing.T) {
	f := newRetryFixture(t)
	f.registry = NewRegistry(nil, auth.NewGate(fullSeatAuthority{}),
		WithIdempotency(f.claims), WithReadCharger(f.charger))
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
	f.claims.verdict = Claim{State: ClaimReplay, Result: json.RawMessage(fmt.Sprintf(
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

// Idempotency is a retry-safety LAYER. Refusing the call because the layer
// hiccupped would leave the caller retrying something now less protected than a
// call sent with no key at all — the REST middleware's posture, and the same
// one here.
func TestAClaimStoreFailureRunsTheCallRatherThanRefusingIt(t *testing.T) {
	f := newRetryFixture(t)
	f.claims.claimErr = errors.New("the claim transaction failed")

	if _, err := f.invoke(t, `{"idempotency_key":"k-1"}`); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if f.tool.runs != 1 {
		t.Fatalf("the tool ran %d times, want 1", f.tool.runs)
	}
	if len(f.claims.settled) != 0 {
		t.Fatalf("a claim that was never taken was settled: %v", f.claims.settled)
	}
}

func TestAFailedCallGivesItsKeyBack(t *testing.T) {
	f := newRetryFixture(t)
	f.claims.verdict = Claim{State: ClaimFresh}
	f.tool.fail = errors.New("the provider refused the write")

	if _, err := f.invoke(t, `{"idempotency_key":"k-1"}`); err == nil {
		t.Fatal("the handler's failure was swallowed")
	}
	if len(f.claims.released) != 1 || f.claims.released[0] != "send_email/k-1" {
		t.Fatalf("released = %v, want the key back", f.claims.released)
	}
	if len(f.claims.settled) != 0 {
		t.Fatalf("a failure was recorded for replay: %v", f.claims.settled)
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
	t.Run("releasing fails after a failed call", func(t *testing.T) {
		f := newRetryFixture(t)
		f.claims.verdict = Claim{State: ClaimFresh}
		f.tool.fail = errors.New("the provider refused the write")
		f.claims.releaseErr = errors.New("the delete failed")
		_, err := f.invoke(t, `{"idempotency_key":"k-1"}`)
		if err == nil || !strings.Contains(err.Error(), "provider refused") {
			t.Fatalf("err = %v, want the handler's own failure", err)
		}
	})
}

// Ignoring the key would promise retry safety the surface cannot keep, and the
// caller would never learn otherwise — what it fails to prevent is a second
// irreversible act.
func TestAKeyIsRefusedOnASurfaceThatCannotClaimIt(t *testing.T) {
	f := newRetryFixture(t)
	f.registry = NewRegistry(nil, auth.NewGate(fullSeatAuthority{}), WithReadCharger(f.charger))
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
