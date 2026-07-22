// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// TestProviderWriteVerbsUnsupported proves every write verb declares
// itself unsupported rather than silently no-op or panic — overlay mode
// has no write-back path until branch 2. NewProvider(nil, nil) is
// sufficient because none of these verbs touch the mirror store or the
// freshness reader.
func TestProviderWriteVerbsUnsupported(t *testing.T) {
	p := NewProvider(nil, nil)
	ctx := context.Background()

	t.Run("Create", func(t *testing.T) {
		_, err := p.Create(ctx, datasource.CreateInput{EntityType: datasource.EntityPerson})
		if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Fatalf("want ErrUnsupportedBySoR, got %v", err)
		}
	})

	t.Run("Update", func(t *testing.T) {
		_, err := p.Update(ctx, datasource.UpdateInput{Ref: datasource.EntityRef{Type: datasource.EntityPerson}})
		if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Fatalf("want ErrUnsupportedBySoR, got %v", err)
		}
	})

	t.Run("AdvanceDeal", func(t *testing.T) {
		_, err := p.AdvanceDeal(ctx, datasource.AdvanceDealInput{})
		if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Fatalf("want ErrUnsupportedBySoR, got %v", err)
		}
	})

	t.Run("Archive", func(t *testing.T) {
		_, err := p.Archive(ctx, datasource.EntityRef{Type: datasource.EntityPerson})
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
	for _, class := range IncumbentEngagementClasses {
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

// TestProviderSearchRequiresExactlyOneEntityType proves Search's own
// branch-1 scope guard: any number of entity types other than exactly
// one is a clean error, never a silent "search the first one" guess. A
// zero-value MirrorStore is enough here — the guard runs before Search
// ever touches p.ms.
func TestProviderSearchRequiresExactlyOneEntityType(t *testing.T) {
	p := NewProvider(&MirrorStore{}, nil)

	tests := [][]datasource.EntityType{
		nil,
		{datasource.EntityPerson, datasource.EntityDeal},
	}
	for _, types := range tests {
		_, err := p.Search(context.Background(), datasource.SearchQuery{EntityTypes: types})
		if err == nil {
			t.Fatalf("Search with %d entity types: want an error, got nil", len(types))
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
