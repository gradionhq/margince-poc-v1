// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the ingress port refuses, and what it stamps — every one of these
// answerable with no database, which is the property being asserted as much as
// the answers themselves: a call that should not reach capture must not reach a
// pool first.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// composeIngressFor publishes a composed set in which one unit declares one
// ingress source, and restores whatever was composed before.
//
// It writes the same store RegisterExtensions writes, because that store is
// what the port reads: a second copy kept for tests could describe a unit that
// is not serving, which is the reason composedIngressFor reads this one.
func composeIngressFor(t *testing.T, unit string, sources ...extension.IngressSource) {
	t.Helper()
	composeCapturingUnit(t, unit, nil, sources...)
}

// composeCapturingUnit is the same, for a unit that also SUPPLIES a transport.
// The two are one function because what a unit may file is decided by both
// declarations together: the source it captures from, and the channel it is
// allowed to name on a message.
func composeCapturingUnit(t *testing.T, unit string, channels []extension.Channel, sources ...extension.IngressSource) {
	t.Helper()
	composeUnit(t, extension.Extension{
		Name:     extension.Name(unit),
		Version:  "1.0.0",
		Ingress:  sources,
		Channels: channels,
		// The user-scoped credential a member deposits, declared — because
		// what the port reads as consent is a secret under a key this unit
		// DECLARES, and a composed set without one describes a unit no member
		// can consent to.
		Secrets: []extension.SecretsRequest{
			{Key: ingressTestSecretKey, Scope: extension.SecretScopeUser},
		},
	})
}

// ingressTestSecretKey is the declared key the probe unit's members deposit
// their credential under.
const ingressTestSecretKey = "api-token"

// composeUnit publishes one composed unit and restores what was composed
// before, rather than clearing: a test that cleared would leave a sibling
// describing an installation that composes nothing.
func composeUnit(t *testing.T, unit extension.Extension) {
	t.Helper()
	previous := ComposedExtensions()
	setComposedExtensions([]extension.Extension{unit})
	t.Cleanup(func() { setComposedExtensions(previous) })
}

// A unit that declares no user-scoped secret has no way for a member to consent
// to it, so there is nobody it may act for — and the refusal is answered
// without a query, because the composed declaration already settles it.
func TestAUnitWithNoUserScopedSecretCanActForNobody(t *testing.T) {
	composeUnit(t, extension.Extension{
		Name:    "probe-unit",
		Version: "1.0.0",
		Ingress: []extension.IngressSource{{
			System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
		}},
	})
	_, err := ingestingRuntime(t).Ingest(context.Background(),
		extension.UserID(ids.NewV7().String()), aRecord())
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden — consent is a credential under a DECLARED key, and this unit declares none", err)
	}
}

// ingestingRuntime is a unit's Runtime as an unattended run holds it: live,
// nobody behind it, wired to a role that composes capture.
//
// The pool is a bare non-nil handle. Every test in this file asserts a refusal
// that must be answered BEFORE a query, so a pool that could serve one would
// weaken the assertion rather than strengthen it — if any of these ever reached
// the database, the test would panic instead of passing.
func ingestingRuntime(t *testing.T) *callRuntime {
	t.Helper()
	rt := unattendedRuntimeFor(
		principal.WithWorkspaceID(context.Background(), ids.NewV7()),
		"probe-unit", "1.0.0", "job", extensionRuntimeBinding{
			pool:        &pgxpool.Pool{},
			captureSink: &capture.Sink{},
		},
	)
	return rt
}

// aRecord is one well-formed record, so a test that is about a refusal changes
// exactly the thing it is about.
func aRecord() extension.Record {
	return extension.Record{
		System: "probe-system",
		Key:    "42",
		Activity: extension.ActivityFields{
			Kind:       "note",
			Subject:    "a directed message",
			Body:       "the body",
			OccurredAt: time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC),
			Direction:  extension.DirectionInbound,
		},
		ThreadKey:    "probe:1:2",
		Counterparty: extension.Counterparty{Email: "outside@example.com", Domain: "example.com", Direction: extension.DirectionInbound},
		Addresses:    []string{"outside@example.com", "member@installation.test"},
		Raw:          []byte(`{"id":42}`),
	}
}

// A unit reaches core capture only where an operator can see that it does. Both
// arms are the same rule from opposite sides: nothing declared, and a name that
// is not the declared one — which is what a typo looks like, and the reason it
// must be a refusal rather than a second provenance namespace nobody knows
// exists.
func TestAnUndeclaredIngressSourceIsRefused(t *testing.T) {
	for name, declared := range map[string][]extension.IngressSource{
		"a unit that declared no ingress at all": nil,
		"a unit that declared another source": {{
			System: "another-system", Lands: []extension.RecordKind{extension.KindActivity},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			composeIngressFor(t, "probe-unit", declared...)
			_, err := ingestingRuntime(t).Ingest(context.Background(), extension.UserID(ids.NewV7().String()), aRecord())
			if !errors.Is(err, extension.ErrIngressNotDeclared) {
				t.Fatalf("err = %v, want ErrIngressNotDeclared", err)
			}
		})
	}
}

// A declaration belongs to the unit that made it. Reading the composed set by
// name is what keeps one unit from landing records under another's provenance —
// the ledger's answer to "which unit produced this" is only as good as this
// lookup.
func TestAnIngressSourceBelongsToTheUnitThatDeclaredIt(t *testing.T) {
	composeIngressFor(t, "another-unit", extension.IngressSource{
		System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
	})
	_, err := ingestingRuntime(t).Ingest(context.Background(), extension.UserID(ids.NewV7().String()), aRecord())
	if !errors.Is(err, extension.ErrIngressNotDeclared) {
		t.Fatalf("err = %v, want ErrIngressNotDeclared — a source declared by a sibling unit is not this unit's", err)
	}
}

// The authority rule, and the one that kills the confused deputy: an invocation
// that has a caller has two authorities in play. Refused before anything is
// spent, which is why this test needs no database either.
func TestAnAttendedInvocationCannotIngest(t *testing.T) {
	composeIngressFor(t, "probe-unit", extension.IngressSource{
		System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
	})
	rt := ingestingRuntime(t)
	rt.unattended = false

	_, err := rt.Ingest(context.Background(), extension.UserID(ids.NewV7().String()), aRecord())
	if !errors.Is(err, extension.ErrAttendedIngest) {
		t.Fatalf("err = %v, want ErrAttendedIngest", err)
	}
}

// The nesting refusal, probed on the interleaving a BOOLEAN would have got
// wrong rather than on straight-line nesting.
//
// Two transactions are open on one Runtime — legitimate, a handler may hold
// them concurrently — and one of them returns. A flag set at the first acquire
// and cleared at the first release would now read "no transaction open" while
// the second still holds a connection, and an ingest would pass the check and
// then wait for a connection this Runtime is holding. On a small pool that does
// not fail; it hangs.
func TestAnIngestInsideTheUnitsOwnTransactionIsRefused(t *testing.T) {
	composeIngressFor(t, "probe-unit", extension.IngressSource{
		System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
	})
	rt := ingestingRuntime(t)
	member := extension.UserID(ids.NewV7().String())

	mustEnterTx(t, rt)
	if _, err := rt.Ingest(context.Background(), member, aRecord()); !errors.Is(err, extension.ErrNestedIngest) {
		t.Fatalf("err = %v, want ErrNestedIngest while one transaction is open", err)
	}
	mustEnterTx(t, rt)
	rt.leaveTx()
	if _, err := rt.Ingest(context.Background(), member, aRecord()); !errors.Is(err, extension.ErrNestedIngest) {
		t.Fatalf("err = %v, want ErrNestedIngest while a SECOND transaction is still open — a flag would have been cleared by the first one returning", err)
	}
	rt.leaveTx()
	// And it lifts: a counter that only ever counted up would refuse every
	// ingest a unit made after its first transaction, forever. Probed with a
	// record the grammar refuses, because that answer comes from the step
	// AFTER this one — so it says the nesting gate let the call past without
	// this test needing a database to prove it.
	unkeyed := aRecord()
	unkeyed.Key = ""
	if _, err := rt.Ingest(context.Background(), member, unkeyed); !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("err = %v, want the next refusal along — the nesting one outlived the transactions it is about", err)
	}
}

// mustEnterTx claims a transaction slot, failing the test if the runtime
// refuses — which it does while an ingest is in flight, and which no case here
// is set up to meet.
func mustEnterTx(t *testing.T, rt *callRuntime) {
	t.Helper()
	if err := rt.enterTx(); err != nil {
		t.Fatalf("claiming a transaction slot: %v", err)
	}
}

// The refusal in the OTHER direction, which is what makes the first one a
// guarantee rather than a check-then-use race: while an ingest is in flight,
// this Runtime admits no transaction. Without it, an ingest that had passed its
// check could still have capture's own acquire land after a sibling goroutine
// took the connection.
func TestAUnitCannotOpenATransactionWhileItIsIngesting(t *testing.T) {
	composeIngressFor(t, "probe-unit", extension.IngressSource{
		System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
	})
	rt := ingestingRuntime(t)
	if err := rt.beginIngest(); err != nil {
		t.Fatalf("claiming the ingest slot: %v", err)
	}
	defer rt.endIngest()

	if err := rt.enterTx(); !errors.Is(err, extension.ErrNestedIngest) {
		t.Fatalf("err = %v, want the transaction refused while an ingest is in flight", err)
	}
}

// A role that composes no capture pipeline has nowhere to put a record, and
// says so by name. The alternative this refusal exists against is worse than an
// error: a sink assembled at the call from the pool alone would compile, run,
// land activities and silently create no people.
func TestARoleThatComposedNoCaptureRefusesByName(t *testing.T) {
	composeIngressFor(t, "probe-unit", extension.IngressSource{
		System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
	})
	rt := ingestingRuntime(t)
	rt.deps.captureSink = nil

	_, err := rt.Ingest(context.Background(), extension.UserID(ids.NewV7().String()), aRecord())
	if !errors.Is(err, errIngressUnwired) {
		t.Fatalf("err = %v, want errIngressUnwired", err)
	}
}

// The lifetime contract holds here as it does on every other capability: a
// Runtime a handler stashed and used after its call is over ingests nothing.
func TestAReleasedRuntimeCannotIngest(t *testing.T) {
	composeIngressFor(t, "probe-unit", extension.IngressSource{
		System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
	})
	rt := ingestingRuntime(t)
	rt.release()

	_, err := rt.Ingest(context.Background(), extension.UserID(ids.NewV7().String()), aRecord())
	if !errors.Is(err, extension.ErrRuntimeExpired) {
		t.Fatalf("err = %v, want ErrRuntimeExpired", err)
	}
}

// The port runs the published Record.Validate before it spends anything, so a
// record the core would refuse costs no transaction. The exhaustive arms of
// that grammar are its own package's tests; what is asserted here is that this
// side calls it, and answers the published class.
func TestARecordTheGrammarRefusesNeverReachesCapture(t *testing.T) {
	composeIngressFor(t, "probe-unit", extension.IngressSource{
		System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
	})
	unkeyed := aRecord()
	unkeyed.Key = ""

	_, err := ingestingRuntime(t).Ingest(context.Background(), extension.UserID(ids.NewV7().String()), unkeyed)
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// The member is named by the unit, so the name is checked before it is used —
// and a value that is not an id at all is the unit's mistake, answered as one.
func TestAMemberIdThatIsNotAnIdIsRefusedBeforeAnyQuery(t *testing.T) {
	composeIngressFor(t, "probe-unit", extension.IngressSource{
		System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
	})
	_, err := ingestingRuntime(t).Ingest(context.Background(), "the-member-i-mean", aRecord())
	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// Provenance is what a unit does not get to state, and this is the assertion
// that it cannot: the two identity fields are derived from the invoking unit
// and its DECLARED source, and the natural key's system half with them.
//
// CapturedBy is also the acting principal's id, which is what makes capture's
// own "a connector cannot claim to be another one" check pass by construction
// rather than by this side remembering to keep the two equal.
func TestProvenanceIsStampedFromTheUnitAndItsDeclaredSource(t *testing.T) {
	rt := ingestingRuntime(t)
	normalized := rt.normalized(aRecord())

	if want := "ext:probe-unit:probe-system"; normalized.Source != want {
		t.Errorf("source = %q, want %q", normalized.Source, want)
	}
	if want := "connector:ext:probe-unit"; normalized.CapturedBy != want {
		t.Errorf("captured_by = %q, want %q", normalized.CapturedBy, want)
	}
	if normalized.NaturalKey.SourceSystem != normalized.Source {
		t.Errorf("natural key system = %q, want the derived source %q — two spellings of one provenance is how a replay stops being a no-op",
			normalized.NaturalKey.SourceSystem, normalized.Source)
	}
	if normalized.NaturalKey.SourceID != "42" {
		t.Errorf("natural key id = %q, want the unit's own key", normalized.NaturalKey.SourceID)
	}
	if normalized.EntityType != datasource.EntityActivity {
		t.Errorf("entity type = %q, want an activity", normalized.EntityType)
	}
	// The absence that closes the existence oracle: what a record is about is
	// decided by the core's counterparty resolution, never by a unit naming
	// rows.
	if len(normalized.Links) != 0 {
		t.Errorf("the envelope carries %d link(s) — a unit naming core rows turns the link-visibility probe into a per-row existence oracle", len(normalized.Links))
	}
}

// Everything a unit sends that the core keeps has to survive the conversion.
// The risk is not a field that fails to compile; it is one that crosses in
// principle and is dropped in fact, which is the same class the core port's
// bridge test exists for.
func TestTheConversionCarriesAWholeRecord(t *testing.T) {
	rec := aRecord()
	normalized := ingestingRuntime(t).normalized(rec)

	// NormalizedRecord.Fields is the envelope's `any` — one shape per entity
	// type — so what a landed activity is made of is only readable after this
	// assertion, which is itself the first thing worth pinning: the ingress
	// port hands capture the activity shape and nothing else.
	fields, ok := normalized.Fields.(capture.ActivityFields)
	if !ok {
		t.Fatalf("fields = %T, want capture.ActivityFields", normalized.Fields)
	}
	switch {
	case fields.Kind != rec.Activity.Kind:
		t.Errorf("kind = %q, want %q", fields.Kind, rec.Activity.Kind)
	case fields.Subject != rec.Activity.Subject:
		t.Errorf("subject = %q, want %q", fields.Subject, rec.Activity.Subject)
	case fields.Body != rec.Activity.Body:
		t.Errorf("body = %q, want %q", fields.Body, rec.Activity.Body)
	case !fields.OccurredAt.Equal(rec.Activity.OccurredAt):
		t.Errorf("occurred_at = %v, want %v", fields.OccurredAt, rec.Activity.OccurredAt)
	case fields.Direction != rec.Activity.Direction:
		t.Errorf("direction = %q, want %q", fields.Direction, rec.Activity.Direction)
	case normalized.ThreadKey != rec.ThreadKey:
		t.Errorf("thread key = %q, want %q", normalized.ThreadKey, rec.ThreadKey)
	case string(normalized.Raw) != string(rec.Raw):
		t.Errorf("raw = %q, want the provider's own record", normalized.Raw)
	}
	if len(normalized.Addresses) != len(rec.Addresses) {
		t.Fatalf("addresses = %v, want both ends — an empty set reads as 'this connector could not enumerate the parties', which silently disables the internal-only gate",
			normalized.Addresses)
	}
	if normalized.Counterparty.Email != rec.Counterparty.Email ||
		normalized.Counterparty.Domain != rec.Counterparty.Domain ||
		normalized.Counterparty.Direction != rec.Counterparty.Direction {
		t.Errorf("counterparty = %+v, want %+v", normalized.Counterparty, rec.Counterparty)
	}
}

// The refusal classes are a MAPPING, and the property that matters is what does
// not survive it: capture's errors carry table names, constraint names and SQL
// state, and a unit is other people's code.
func TestIngressMapsRefusalsAndLeaksNoDetail(t *testing.T) {
	rt := ingestingRuntime(t)
	for name, probe := range map[string]struct {
		in   error
		want error
	}{
		"permission denied": {apperrors.ErrPermissionDenied, extension.ErrForbidden},
		"not found":         {apperrors.ErrNotFound, extension.ErrNotFound},
		"conflict":          {apperrors.ErrConflict, extension.ErrConflict},
		"version skew":      {apperrors.ErrVersionSkew, extension.ErrConflict},
	} {
		t.Run(name, func(t *testing.T) {
			if got := rt.ingressRefusal(context.Background(), probe.in); !errors.Is(got, probe.want) {
				t.Errorf("ingressRefusal(%v) = %v, want %v", probe.in, got, probe.want)
			}
		})
	}
	if got := rt.ingressRefusal(context.Background(), nil); got != nil {
		t.Errorf("ingressRefusal(nil) = %v, want nil — a mapper that invents an error on the success path refuses every landing", got)
	}
	leaky := errors.New(`insert into "activity" violates constraint activity_source_system_key`)
	mapped := rt.ingressRefusal(context.Background(), leaky)
	if mapped == nil {
		t.Fatal("an unclassified fault was mapped to success")
	}
	if strings.Contains(mapped.Error(), "activity") || strings.Contains(mapped.Error(), "constraint") {
		t.Errorf("the core's own error text reached the unit: %v", mapped)
	}
}

// The boot preflight, which is where a declaration an operator will read is
// held to the published grammar — and where two entries for one provenance
// namespace are refused, since which of them the port answers from would
// otherwise be declaration order.
func TestThePreflightHoldsIngressDeclarationsToTheirGrammar(t *testing.T) {
	valid := extension.Extension{
		Name: "probe-unit", Version: "1.0.0",
		Ingress: []extension.IngressSource{{
			System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity},
		}},
	}
	if err := preflightIngress(valid); err != nil {
		t.Fatalf("a well-formed declaration was refused: %v", err)
	}

	twice := valid
	twice.Ingress = append(slices.Clone(valid.Ingress), valid.Ingress[0])
	if err := preflightIngress(twice); err == nil {
		t.Error("one system declared twice was accepted")
	}

	ungrammatical := valid
	ungrammatical.Ingress = []extension.IngressSource{{System: "Probe System"}}
	if err := preflightIngress(ungrammatical); err == nil {
		t.Error("a system key the published grammar refuses was accepted")
	}
}
