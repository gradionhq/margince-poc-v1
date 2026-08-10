// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// TestProviderUnsupportedVerbs proves the verbs overlay V1 genuinely
// cannot project onto a single incumbent-first write declare themselves
// unsupported rather than silently no-op or panic (OVA-MAP-W6):
// AdvanceDeal (blocked on the overlay stage-map substrate StageSemantic
// also lacks), and Merge/PromoteLead (cross-aggregate lifecycle
// orchestrations with no atomic incumbent projection). Create/Update/
// Archive ARE supported — see the object-gate and write-back tests below.
func TestProviderUnsupportedVerbs(t *testing.T) {
	p := NewProvider(nil, nil)
	ctx := context.Background()

	t.Run("AdvanceDeal", func(t *testing.T) {
		_, err := p.AdvanceDeal(ctx, datasource.AdvanceDealInput{})
		if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Fatalf("want ErrUnsupportedBySoR, got %v", err)
		}
	})

	t.Run("Merge", func(t *testing.T) {
		_, err := p.Merge(ctx, datasource.MergeInput{Type: datasource.EntityPerson})
		if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Fatalf("want ErrUnsupportedBySoR, got %v", err)
		}
	})

	t.Run("PromoteLead", func(t *testing.T) {
		_, merged, err := p.PromoteLead(ctx, ids.NewV7(), "manual", nil)
		if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Fatalf("want ErrUnsupportedBySoR, got %v", err)
		}
		if merged {
			t.Fatal("an unsupported call must never report merged=true")
		}
	})
}

// TestProviderWriteVerbsObjectGateBeforeTheIncumbent proves the write
// verbs apply object RBAC (auth.Require) BEFORE reaching the incumbent —
// the same MCP-bypass closure the read verbs carry: a bound principal
// whose role grants no write capability is refused with
// ErrPermissionDenied, and the incumbent is never touched (the nil
// resolver would otherwise error differently). auth.Require runs first,
// so this stays a pure unit test.
func TestProviderWriteVerbsObjectGateBeforeTheIncumbent(t *testing.T) {
	p := NewProvider(&MirrorStore{}, nil)
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:no-grants",
		Permissions: principal.Permissions{RoleKeys: []string{"rep"}},
	})
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}

	// Create is not gated on the grant at all: the provider declares the verb
	// unsupported for every type (SupportsWrite), and a capability the mirror
	// does not have is refused before any principal is consulted.
	if _, err := p.Create(ctx, datasource.CreateInput{EntityType: datasource.EntityPerson}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("Create in overlay: err = %v, want ErrUnsupportedBySoR", err)
	}
	if _, err := p.Update(ctx, datasource.UpdateInput{Ref: ref}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Update without a person update grant: err = %v, want ErrPermissionDenied", err)
	}
	if _, err := p.Archive(ctx, ref); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Archive without a person delete grant: err = %v, want ErrPermissionDenied", err)
	}
}

// TestProviderWriteWithoutIncumbentResolver proves a permitted write
// against a Provider with no incumbent write resolver wired fails with a
// clear configuration error — never a nil-pointer panic and never a
// misleading ErrUnsupportedBySoR (update IS a supported verb; the resolver
// is simply absent).
func TestProviderWriteWithoutIncumbentResolver(t *testing.T) {
	p := NewProvider(&MirrorStore{}, nil)
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:granted",
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"person": {Create: true, Update: true, Delete: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	_, err := p.Update(ctx, datasource.UpdateInput{
		Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()},
	})
	if err == nil || errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("Update with no resolver: err = %v, want a clear configuration error", err)
	}
}

// TestProviderRunReportUnsupported proves RunReport declares itself
// unsupported — HubSpot has no run_report analogue (design.md §4.5).
// TestProviderReadVerbsObjectGateBeforeTheMirror proves the read verbs
// apply object RBAC (auth.Require ActionRead) like the native stores: a
// bound principal whose role grants no object capability is refused with
// ErrPermissionDenied. This closes the MCP read_record/search_records
// bypass — those tools reach the provider directly, without the REST
// shadow's gate. auth.Require runs before any mirror access, so a denied
// actor never reaches the DB-backed store; this stays a pure unit test.
func TestProviderReadVerbsObjectGateBeforeTheMirror(t *testing.T) {
	p := NewProvider(&MirrorStore{}, nil)
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:no-grants",
		Permissions: principal.Permissions{RoleKeys: []string{"rep"}},
	})
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}
	if _, err := p.Read(ctx, ref); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Read without a person read grant: err = %v, want ErrPermissionDenied", err)
	}
	if _, err := p.Search(ctx, datasource.SearchQuery{
		EntityTypes: []datasource.EntityType{datasource.EntityPerson},
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Search without a person read grant: err = %v, want ErrPermissionDenied", err)
	}
	if _, err := p.ListFields(ctx, datasource.EntityPerson); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("ListFields without a person read grant: err = %v, want ErrPermissionDenied", err)
	}
	// Freshness belongs in this list: a force-fresh answer spends a real
	// incumbent call against the record, so it is as much a read as the three
	// above and reaches the provider by the same ungated MCP path.
	if _, err := p.Freshness(ctx, ref); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Freshness without a person read grant: err = %v, want ErrPermissionDenied", err)
	}
}

func TestProviderRunReportUnsupported(t *testing.T) {
	p := NewProvider(nil, nil)
	_, err := p.RunReport(context.Background(), datasource.ReportPlan{Entity: datasource.EntityDeal})
	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("want ErrUnsupportedBySoR, got %v", err)
	}
}

// TestProviderStageSemanticUnsupported proves StageSemantic declares
// itself unsupported: no incumbent stage-mapping data source is wired
// to this seam yet (see the StageSemantic doc comment in provider.go).
func TestProviderStageSemanticUnsupported(t *testing.T) {
	p := NewProvider(nil, nil)
	_, _, err := p.StageSemantic(context.Background(), ids.NewV7())
	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("want ErrUnsupportedBySoR, got %v", err)
	}
}

// TestProviderReadRequiresAMirrorStore proves the honest-hard-case
// guard: a Provider built with a nil MirrorStore never nil-panics on a
// read verb, it answers a clear, actionable error. The mirror-backed
// success path (Authoritative:false + the mirror's LastSyncedAt) is
// covered by TestProviderReadServesFromTheMirror, gated behind
// //go:build integration since MirrorStore.Get needs a real, migrated
// Postgres (RLS + the visibility deny-join).
func TestProviderReadRequiresAMirrorStore(t *testing.T) {
	p := NewProvider(nil, nil)
	_, err := p.Read(context.Background(), datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()})
	if err == nil {
		t.Fatal("want an error, got nil")
	}
}

// TestProviderFreshnessRequiresAMirrorStoreOrReader proves Freshness
// never nil-panics when both the mirror store and the freshness reader
// are nil — NewProvider(nil, nil) must still answer an error, not crash.
func TestProviderFreshnessRequiresAMirrorStoreOrReader(t *testing.T) {
	p := NewProvider(nil, nil)
	_, err := p.Freshness(context.Background(), datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()})
	if err == nil {
		t.Fatal("want an error, got nil")
	}
}

// TestExternalIDUUIDBridgeRoundTrips proves the numeric external-id<->
// ids.UUID bridge (provider.go) is exactly reversible for HubSpot's own
// decimal-numeric object ids — the shape Read/Search/Freshness all rely
// on to satisfy the frozen EntityRef.ID type against the mirror's
// string-keyed natural key.
func TestExternalIDUUIDBridgeRoundTrips(t *testing.T) {
	// Bare numeric ids (contacts/companies/deals/leads) AND the OVA-MAP-7
	// class-namespaced activity ids both round-trip exactly.
	ids := []string{"0", "1", "100214862042", "18446744073709551615"}
	for _, class := range incumbentEngagementClasses {
		ids = append(ids, class+":123", class+":0")
	}
	for _, externalID := range ids {
		id, err := externalIDToUUID(externalID)
		if err != nil {
			t.Fatalf("externalIDToUUID(%q): %v", externalID, err)
		}
		got := uuidToExternalID(id)
		if got != externalID {
			t.Fatalf("round trip: externalIDToUUID(%q) -> uuidToExternalID = %q", externalID, got)
		}
	}
}

// TestExternalIDUUIDBridgeNamespaceAvoidsCrossClassCollision is the OVA-MAP-7
// proof at the identity bridge: two activities from different engagement
// classes that share a numeric HubSpot id (unique only per-type) must bridge
// to DISTINCT UUIDs, so neither overwrites the other on the mirror key.
func TestExternalIDUUIDBridgeNamespaceAvoidsCrossClassCollision(t *testing.T) {
	callID, err := externalIDToUUID("calls:123")
	if err != nil {
		t.Fatalf("externalIDToUUID(calls:123): %v", err)
	}
	meetingID, err := externalIDToUUID("meetings:123")
	if err != nil {
		t.Fatalf("externalIDToUUID(meetings:123): %v", err)
	}
	if callID == meetingID {
		t.Fatal("calls:123 and meetings:123 bridged to the SAME UUID — a cross-class collision (OVA-MAP-7)")
	}
	// The bare numeric id (a contact) must not collide with either namespaced
	// activity carrying the same number.
	bare, err := externalIDToUUID("123")
	if err != nil {
		t.Fatalf("externalIDToUUID(123): %v", err)
	}
	if bare == callID || bare == meetingID {
		t.Fatal("a bare id 123 collided with a namespaced activity id sharing the number")
	}
}

// TestExternalIDUUIDBridgeRejectsUnknownActivityClass proves an id naming a
// class this build does not know is a clean error, never a silently-wrong
// bridge.
func TestExternalIDUUIDBridgeRejectsUnknownActivityClass(t *testing.T) {
	if _, err := externalIDToUUID("widgets:123"); err == nil {
		t.Fatal("externalIDToUUID(widgets:123): want an error for an unknown activity class, got nil")
	}
}

// TestProviderSearchRefusesATypeTheMirrorCannotHold proves the sweep's own
// vocabulary guard: the mirror carries five object classes, and a query
// naming a sixth is refused rather than walked past. Walking past it would
// answer an empty page about the RECORDS when the truth is about the mode.
// A zero-value MirrorStore is enough — the guard runs before p.ms is touched.
func TestProviderSearchRefusesATypeTheMirrorCannotHold(t *testing.T) {
	p := NewProvider(&MirrorStore{}, nil)

	_, err := p.Search(context.Background(), datasource.SearchQuery{
		EntityTypes: []datasource.EntityType{datasource.EntityPerson, datasource.EntityProject},
	})
	var unsupported *datasource.UnsupportedEntityError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Search naming an unmirrored type = %v, want an UnsupportedEntityError", err)
	}
	if unsupported.Type != string(datasource.EntityProject) {
		t.Errorf("the refusal names %q, want the type the mirror cannot hold", unsupported.Type)
	}
}

// TestASweepCursorNamesAPositionInTheMirrorRatherThanInOneRequest pins the
// resume token and the reason it names a TYPE rather than an index: the same
// token has to mean the same place when the request presenting it is not the
// one that minted it.
func TestASweepCursorNamesAPositionInTheMirrorRatherThanInOneRequest(t *testing.T) {
	minted, err := storekit.EncodeSweepCursor(storekit.SweepCursor{Stream: string(datasource.EntityOrganization), Inner: "mirror-42"})
	if err != nil {
		t.Fatalf("encoding a sweep position: %v", err)
	}
	resumeAt, inner, err := resumeStream(minted)
	if err != nil {
		t.Fatalf("decoding a cursor the sweep minted: %v", err)
	}
	if resumeAt != datasource.EntityOrganization || inner != "mirror-42" {
		t.Errorf("resume position = (%q, %q), want the organization stream at its own mirror cursor", resumeAt, inner)
	}

	// An empty cursor is the start of the walk, not a malformed one.
	if resumeAt, inner, err = resumeStream(""); err != nil || resumeAt != "" || inner != "" {
		t.Errorf("the empty cursor decoded to (%q, %q, %v), want the beginning", resumeAt, inner, err)
	}

	// Malformed is reserved for a token this package could not have minted.
	for _, probe := range []struct{ name, cursor string }{
		{"not base64 at all", "not a cursor!!"},
		{"base64 of something that is not a position", base64.RawURLEncoding.EncodeToString([]byte("nonsense"))},
		{"naming an object class the mirror cannot hold", mustEncodeSweepCursor(t, datasource.EntityProject)},
	} {
		t.Run(probe.name, func(t *testing.T) {
			_, _, err := resumeStream(probe.cursor)
			var malformed *storekit.MalformedCursorError
			if !errors.As(err, &malformed) {
				t.Errorf("decoding a cursor %s = %v, want the malformed-cursor answer", probe.name, err)
			}
		})
	}
}

// mustEncodeSweepCursor mints a position for a probe, failing the test rather
// than swallowing an encoding error into an empty cursor.
func mustEncodeSweepCursor(t *testing.T, et datasource.EntityType) string {
	t.Helper()
	cursor, err := storekit.EncodeSweepCursor(storekit.SweepCursor{Stream: string(et), Inner: "mirror-7"})
	if err != nil {
		t.Fatalf("encoding a sweep position for %s: %v", et, err)
	}
	return cursor
}

// TestAResumedSweepSurvivesTheWalkChangingUnderIt is the half a cursor over a
// per-request slice could not answer. A caller's readable types change between
// pages — a narrowed `types`, a revoked grant — and the position they hold is
// still a token this server minted.
func TestAResumedSweepSurvivesTheWalkChangingUnderIt(t *testing.T) {
	all := []datasource.EntityType{
		datasource.EntityPerson, datasource.EntityOrganization,
		datasource.EntityDeal, datasource.EntityLead, datasource.EntityActivity,
	}
	for _, probe := range []struct {
		name     string
		walk     []datasource.EntityType
		resumeAt datasource.EntityType
		want     int
	}{
		{"the type is still walked", all, datasource.EntityDeal, 2},
		{"no cursor starts at the beginning", all, "", 0},
		{
			"the type was narrowed away — resume PAST it, never before",
			[]datasource.EntityType{datasource.EntityPerson, datasource.EntityLead},
			datasource.EntityDeal, 1,
		},
		{
			"everything past the position was narrowed away — the walk is over",
			[]datasource.EntityType{datasource.EntityPerson},
			datasource.EntityLead, 1,
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if at := resumePosition(probe.walk, probe.resumeAt); at != probe.want {
				t.Errorf("resumePosition = %d, want %d — resuming before the position re-serves rows the "+
					"caller already holds, and past the next one hides rows they never saw", at, probe.want)
			}
		})
	}
}

// A type named twice is walked once. The contract's `types` is a plain array
// with no uniqueness rule, and a stream walked twice serves every record in it
// twice — a cursor names the type, not which of its appearances.
func TestASweepWalksEachTypeOnceInMirrorOrder(t *testing.T) {
	walk, err := searchableTypes([]datasource.EntityType{
		datasource.EntityDeal, datasource.EntityPerson, datasource.EntityDeal,
	})
	if err != nil {
		t.Fatalf("resolving the walk: %v", err)
	}
	want := []datasource.EntityType{datasource.EntityPerson, datasource.EntityDeal}
	if !slices.Equal(walk, want) {
		t.Errorf("walk = %v, want %v — each type once, in the mirror's own order", walk, want)
	}
}

// A structured filter the mirror cannot evaluate is refused, not dropped.
//
// Dropping it would answer the unnarrowed page — a SUPERSET of what was asked
// for, wearing the shape of the right answer — which is the silent break
// AC-OV-2 forbids. The guard runs before the store is touched, so a zero-value
// MirrorStore is enough to prove it, and the refusal is the declared
// unsupported-by-SoR sentinel rather than a generic error, because a caller
// branches on it to say "not available here" instead of "this failed".
func TestProviderSearchRefusesAFilterTheMirrorCannotAnswer(t *testing.T) {
	p := NewProvider(&MirrorStore{}, nil)
	// A caller who MAY read. The refusal sits behind the object gate on
	// purpose — see below — so an ungranted principal here would be refused for
	// a reason that has nothing to do with filters.
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:reader",
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"deal": {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})

	_, err := p.Search(ctx, datasource.SearchQuery{
		EntityTypes: []datasource.EntityType{datasource.EntityDeal},
		Filters:     map[string]string{"stage_id": ids.NewV7().String()},
	})

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("Search with a filter: got %v, want %v — a dropped filter answers a wider question",
			err, apperrors.ErrUnsupportedBySoR)
	}
}

// And a caller who may NOT read hears the object gate, filter or no filter.
//
// The order matters more than it looks: refusing the filter first would let an
// unauthorized caller learn this workspace's system-of-record mode by attaching
// one, since the two refusals are different words.
func TestProviderSearchRefusesAnUngrantedCallerBeforeItsFilters(t *testing.T) {
	p := NewProvider(&MirrorStore{}, nil)
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:no-grants",
		Permissions: principal.Permissions{RoleKeys: []string{"rep"}},
	})

	for _, filters := range []map[string]string{nil, {"stage_id": ids.NewV7().String()}} {
		_, err := p.Search(ctx, datasource.SearchQuery{
			EntityTypes: []datasource.EntityType{datasource.EntityDeal}, Filters: filters,
		})
		if !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Errorf("Search with %d filters and no read grant: got %v, want %v",
				len(filters), err, apperrors.ErrPermissionDenied)
		}
	}
}

// TestProviderSearchRequiresAMirrorStore proves Search's own nil-store
// guard, mirroring TestProviderReadRequiresAMirrorStore.
func TestProviderSearchRequiresAMirrorStore(t *testing.T) {
	p := NewProvider(nil, nil)
	_, err := p.Search(context.Background(), datasource.SearchQuery{EntityTypes: []datasource.EntityType{datasource.EntityPerson}})
	if err == nil {
		t.Fatal("Search with a nil mirror store: want an error, got nil")
	}
}

// TestProviderListObjectsAndListFieldsRequireAMirrorStore proves the
// remaining read verbs' nil-store guard.
func TestProviderListObjectsAndListFieldsRequireAMirrorStore(t *testing.T) {
	p := NewProvider(nil, nil)
	if _, err := p.ListObjects(context.Background()); err == nil {
		t.Fatal("ListObjects with a nil mirror store: want an error, got nil")
	}
	if _, err := p.ListFields(context.Background(), datasource.EntityPerson); err == nil {
		t.Fatal("ListFields with a nil mirror store: want an error, got nil")
	}
}

// TestMirrorRowMatchesText proves the naive case-insensitive substring
// filter Search applies over a mirror row's string-valued fields —
// including that a non-string field value is skipped rather than
// panicking on a type assertion.
func TestMirrorRowMatchesText(t *testing.T) {
	row := Row{Fields: map[string]any{
		"first_name": "Christian",
		"age":        42.0,
		"nested":     map[string]any{"x": "y"},
	}}
	if !mirrorRowMatchesText(row, "chris") {
		t.Error("mirrorRowMatchesText: want a match on a case-insensitive substring of a string field")
	}
	if mirrorRowMatchesText(row, "nomatch") {
		t.Error("mirrorRowMatchesText: want no match for a substring absent from every string field")
	}
	if mirrorRowMatchesText(row, "42") {
		t.Error("mirrorRowMatchesText: want no match against a non-string field value")
	}
}

// TestInferFieldKind pins every JSON-decoded shape's inferred kind name,
// including the default "unknown" for a shape none of the cases name.
func TestInferFieldKind(t *testing.T) {
	tests := []struct {
		v    any
		want string
	}{
		{"a string", "string"},
		{true, "boolean"},
		{float64(1), "number"},
		{map[string]any{}, "object"},
		{[]any{}, "array"},
		{nil, "unknown"},
		{int(1), "unknown"},
	}
	for _, tt := range tests {
		if got := inferFieldKind(tt.v); got != tt.want {
			t.Errorf("inferFieldKind(%#v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

// TestCapitalize pins capitalize's ASCII-first-byte upper-casing,
// including the empty-string honest hard case.
func TestCapitalize(t *testing.T) {
	if got := capitalize(""); got != "" {
		t.Errorf("capitalize(%q) = %q, want empty", "", got)
	}
	if got := capitalize("person"); got != "Person" {
		t.Errorf("capitalize(%q) = %q, want %q", "person", got, "Person")
	}
}

// TestExternalIDUUIDBridgeRejectsNonNumeric proves a non-numeric
// external id — outside this v1 HubSpot scope assumption — is a clear
// error, never a silently truncated/garbage UUID.
func TestExternalIDUUIDBridgeRejectsNonNumeric(t *testing.T) {
	if _, err := externalIDToUUID("not-a-number"); err == nil {
		t.Fatal("want an error for a non-numeric external id, got nil")
	}
}
