// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

var wireSyncedAt = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

// wireRecord builds a mirror-shaped datasource.Record the way
// overlay.Provider serves one: canonical fields as jsonb, T2-labelled.
func wireRecord(t *testing.T, et datasource.EntityType, fields map[string]any) datasource.Record {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshaling fixture fields: %v", err)
	}
	return datasource.Record{
		Ref:       datasource.EntityRef{Type: et, ID: ids.NewV7()},
		Fields:    raw,
		Freshness: datasource.FreshnessInfo{LastSyncedAt: wireSyncedAt, Authoritative: false},
	}
}

func wireCtx() context.Context {
	return principal.WithWorkspaceID(context.Background(), ids.NewV7())
}

func TestOverlayWirePersonAssemblesNameAndStampsProvenance(t *testing.T) {
	rec := wireRecord(t, datasource.EntityPerson, map[string]any{
		"first_name": "Ada", "last_name": "Overlay", "title": "CTO",
	})
	person, err := overlayWirePerson(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.FullName != "Ada Overlay" {
		t.Errorf("FullName = %q, want the joined first+last", person.FullName)
	}
	if person.Source != "overlay" {
		t.Errorf("Source = %q, want overlay", person.Source)
	}
	if !person.CreatedAt.Equal(wireSyncedAt) || !person.UpdatedAt.Equal(wireSyncedAt) {
		t.Error("timestamps must carry the mirror's own last-synced instant — the only time the mirror can honestly claim")
	}
	if person.Raw == nil || (*person.Raw)["title"] != "CTO" {
		t.Error("the full canonical payload must ride raw")
	}
}

func TestOverlayWirePersonNamelessFallsBackToEmailThenUnnamed(t *testing.T) {
	withEmail := wireRecord(t, datasource.EntityPerson, map[string]any{
		"person_email": []map[string]any{{"email": "ada@example.test", "email_type": "work", "is_primary": true, "position": 0}},
	})
	person, err := overlayWirePerson(wireCtx(), withEmail)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.FullName != "ada@example.test" {
		t.Errorf("nameless person FullName = %q, want the mapped email", person.FullName)
	}
	bare, err := overlayWirePerson(wireCtx(), wireRecord(t, datasource.EntityPerson, map[string]any{}))
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if bare.FullName != "Unnamed" {
		t.Errorf("bare person FullName = %q, want Unnamed", bare.FullName)
	}
}

func TestOverlayWireOrganizationSurfacesDomain(t *testing.T) {
	rec := wireRecord(t, datasource.EntityOrganization, map[string]any{
		"display_name":        "Acme",
		"organization_domain": []map[string]any{{"domain": "acme.io", "is_primary": true, "position": 0}},
	})
	org, err := overlayWireOrganization(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireOrganization: %v", err)
	}
	if org.Domains == nil || len(*org.Domains) != 1 {
		t.Fatalf("Domains = %#v, want exactly one mirrored domain", org.Domains)
	}
	d := (*org.Domains)[0]
	if d.Domain != "acme.io" {
		t.Errorf("domain = %q, want acme.io", d.Domain)
	}
	if !d.IsPrimary {
		t.Error("the single mirrored domain must be primary")
	}
	if d.Source != "overlay" {
		t.Errorf("domain source = %q, want overlay", d.Source)
	}
	if d.Id == (openapi_types.UUID{}) {
		t.Error("the synthesized domain id must not be the zero UUID")
	}
	// The synthesized id is STABLE across reads: an overlay domain has no
	// native row of its own, so a churning id would be a fresh identity on
	// every request.
	again, err := overlayWireOrganization(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireOrganization (second read): %v", err)
	}
	if (*again.Domains)[0].Id != d.Id {
		t.Errorf("domain id churned across reads: %v then %v", d.Id, (*again.Domains)[0].Id)
	}
}

func TestOverlayWireOrganizationWithoutDomainOmitsDomains(t *testing.T) {
	rec := wireRecord(t, datasource.EntityOrganization, map[string]any{"display_name": "Acme"})
	org, err := overlayWireOrganization(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireOrganization: %v", err)
	}
	if org.Domains != nil {
		t.Errorf("Domains = %#v, want nil when the mirror carries no domain", org.Domains)
	}
}

func TestOverlayWireDealDerivesStatusFromClosedStageKeys(t *testing.T) {
	for stage, want := range map[string]crmcontracts.DealStatus{
		"closedwon":      crmcontracts.DealStatusWon,
		"closedlost":     crmcontracts.DealStatusLost,
		"qualifiedtobuy": crmcontracts.DealStatusOpen,
		"":               crmcontracts.DealStatusOpen,
	} {
		rec := wireRecord(t, datasource.EntityDeal, map[string]any{"name": "Acme", "stage_id": stage})
		deal, err := overlayWireDeal(wireCtx(), rec)
		if err != nil {
			t.Fatalf("overlayWireDeal(%q): %v", stage, err)
		}
		if deal.Status != want {
			t.Errorf("stage %q → status %q, want %q", stage, deal.Status, want)
		}
	}
}

func TestOverlayWireDealParsesAmountAndCloseDate(t *testing.T) {
	rec := wireRecord(t, datasource.EntityDeal, map[string]any{
		"name": "Acme", "amount_minor": "125000", "expected_close_date": "2026-09-30",
	})
	deal, err := overlayWireDeal(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireDeal: %v", err)
	}
	if deal.AmountMinor == nil || *deal.AmountMinor != 125000 {
		t.Errorf("AmountMinor = %v, want 125000 (HubSpot amounts arrive as strings)", deal.AmountMinor)
	}
	if deal.ExpectedCloseDate == nil || deal.ExpectedCloseDate.Format("2006-01-02") != "2026-09-30" {
		t.Errorf("ExpectedCloseDate = %v, want 2026-09-30", deal.ExpectedCloseDate)
	}
}

// TestOverlayWireDealNullsPipelineAndStage is the OVA-MAP-6 contract proof:
// an overlay-mirror deal reads with NULL pipeline_id/stage_id (never a
// fabricated/zero UUID — a forbidden dangling FK), while the incumbent's own
// pipeline/dealstage identifiers ride raw.
func TestOverlayWireDealNullsPipelineAndStage(t *testing.T) {
	rec := wireRecord(t, datasource.EntityDeal, map[string]any{
		"name": "Acme", "pipeline_id": "default", "stage_id": "appointmentscheduled",
	})
	deal, err := overlayWireDeal(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireDeal: %v", err)
	}
	if deal.PipelineId != nil {
		t.Errorf("PipelineId = %v, want nil (overlay has no native pipeline row — OVA-MAP-6)", *deal.PipelineId)
	}
	if deal.StageId != nil {
		t.Errorf("StageId = %v, want nil (overlay has no native stage row — OVA-MAP-6)", *deal.StageId)
	}
	// The incumbent identifiers ride raw, never lost.
	if deal.Raw == nil || (*deal.Raw)["pipeline_id"] != "default" || (*deal.Raw)["stage_id"] != "appointmentscheduled" {
		t.Errorf("raw = %v, want the incumbent pipeline/dealstage identifiers preserved", deal.Raw)
	}
}

func TestFieldInt64RejectsNonIntegralNumbers(t *testing.T) {
	for name, v := range map[string]any{
		"fractional": 1.5, "huge": 1e19, "nan": math.NaN(), "inf": math.Inf(1), "text": "12.5",
	} {
		if got, ok := fieldInt64(map[string]any{"amount_minor": v}, "amount_minor"); ok {
			t.Errorf("%s: fieldInt64 = %d, want absent — a narrowed cast invents a different amount", name, got)
		}
	}
	if got, ok := fieldInt64(map[string]any{"amount_minor": float64(42)}, "amount_minor"); !ok || got != 42 {
		t.Errorf("integral float = (%d,%v), want (42,true)", got, ok)
	}
}

func TestOverlayWireActivityKindFallsBackToNoteAndParsesEpochMillis(t *testing.T) {
	rec := wireRecord(t, datasource.EntityActivity, map[string]any{
		"kind": "linkedin_message", "subject": "Ping", "occurred_at": "1767225600000",
	})
	act, err := overlayWireActivity(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireActivity: %v", err)
	}
	if act.Kind != crmcontracts.ActivityKindNote {
		t.Errorf("unknown engagement kind → %q, want note (the true kind stays in raw)", act.Kind)
	}
	if (*act.Raw)["kind"] != "linkedin_message" {
		t.Error("the true engagement kind must survive in raw")
	}
	want := time.UnixMilli(1767225600000).UTC()
	if !act.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want the parsed epoch-millis %v", act.OccurredAt, want)
	}
}

func TestOverlayWireActivityWithoutTimestampFallsBackToSyncInstant(t *testing.T) {
	rec := wireRecord(t, datasource.EntityActivity, map[string]any{"kind": "call"})
	act, err := overlayWireActivity(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireActivity: %v", err)
	}
	if act.Kind != crmcontracts.ActivityKindCall {
		t.Errorf("Kind = %q, want call", act.Kind)
	}
	if !act.OccurredAt.Equal(wireSyncedAt) {
		t.Errorf("OccurredAt = %v, want the sync-instant fallback %v", act.OccurredAt, wireSyncedAt)
	}
}

// TestOverlayWireActivitySurfacesDurationAndDueAt proves the wire assembler
// now consumes the canonical fields the mapping lands: duration_seconds
// (already seconds, OVA-MAP-2 — never re-divided) and a task's due_at
// (OVA-MAP-8), rather than dropping them into raw only.
func TestOverlayWireActivitySurfacesDurationAndDueAt(t *testing.T) {
	rec := wireRecord(t, datasource.EntityActivity, map[string]any{
		"kind": "call", "occurred_at": "2026-06-02T09:00:00.000Z", "duration_seconds": int64(90),
	})
	act, err := overlayWireActivity(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireActivity: %v", err)
	}
	if act.DurationSeconds == nil || *act.DurationSeconds != 90 {
		t.Errorf("DurationSeconds = %v, want 90 (surfaced in seconds, not re-divided)", act.DurationSeconds)
	}

	task := wireRecord(t, datasource.EntityActivity, map[string]any{
		"kind": "task", "occurred_at": "2026-07-01T08:30:00.000Z", "due_at": "2026-07-10T17:00:00.000Z",
	})
	tact, err := overlayWireActivity(wireCtx(), task)
	if err != nil {
		t.Fatalf("overlayWireActivity(task): %v", err)
	}
	if tact.DueAt == nil || !tact.DueAt.Equal(time.Date(2026, 7, 10, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("DueAt = %v, want the task deadline surfaced", tact.DueAt)
	}
}

// TestOverlayWireTitlePrefersCanonicalFullName locks in the search-title
// precedence: when a person carries a canonical full_name that differs from
// first+last (the email-local/placeholder fallback, or an incumbent that set
// full_name independently), the search hit's title is the canonical value —
// matching the person detail — not a separately re-derived name.
func TestOverlayWireTitlePrefersCanonicalFullName(t *testing.T) {
	rec := wireRecord(t, datasource.EntityPerson, map[string]any{
		"full_name": "grace.hopper", "first_name": "", "last_name": "",
		"person_email": []map[string]any{{"email": "grace.hopper@navy.mil", "email_type": "work", "is_primary": true, "position": 0}},
	})
	person, err := overlayWirePerson(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	title := overlayWireTitle(datasource.EntityPerson, *person.Raw)
	if title != "grace.hopper" {
		t.Errorf("search title = %q, want the canonical full_name %q (must match the person detail)", title, "grace.hopper")
	}
	if person.FullName != title {
		t.Errorf("person detail full_name %q and search title %q diverge", person.FullName, title)
	}
}

// The mapper assembles an address into the mirror and it was picked up by
// nothing — the value existed and the slot a client reads stayed empty.
func TestOverlayWirePersonPublishesAddress(t *testing.T) {
	rec := wireRecord(t, datasource.EntityPerson, map[string]any{
		"full_name": "Ada Overlay",
		"address": map[string]any{
			"line1": "Hauptstrasse 1", "city": "Munich",
			"postal_code": "80331", "country": "DE",
		},
	})
	person, err := overlayWirePerson(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.Address == nil {
		t.Fatal("Address is nil; the mapper's address_json assembly must reach the contract's structured slot")
	}
	if person.Address.City == nil || *person.Address.City != "Munich" {
		t.Errorf("Address.City = %v, want Munich", person.Address.City)
	}
	if person.Address.Line1 == nil || *person.Address.Line1 != "Hauptstrasse 1" {
		t.Errorf("Address.Line1 = %v, want the mirrored street", person.Address.Line1)
	}
}

// overlayAddress is the one reader of the canonical address payload, shared
// by the read wire and the flip import. It answers an Address only when a
// member carries something: a blank Address stored on a flipped row, or
// published on a read, asserts a location the incumbent never held.
func TestOverlayAddressCarriesEveryMemberAndOmitsAnEmptyOne(t *testing.T) {
	for name, fields := range map[string]map[string]any{
		"no address key":     {"display_name": "Acme"},
		"address not a map":  {"address": "12 Main St"},
		"empty address map":  {"address": map[string]any{}},
		"only unknown parts": {"address": map[string]any{"floor": "3"}},
	} {
		if got := overlayAddress(fields); got != nil {
			t.Errorf("%s: overlayAddress = %+v, want nil", name, got)
		}
	}

	full := overlayAddress(map[string]any{"address": map[string]any{
		"line1": "12 Main St", "city": "Frankfurt", "region": "HE",
		"postal_code": "60311", "country": "DE",
	}})
	if full == nil {
		t.Fatal("a populated mirrored address must reach the contract's Address")
	}
	// VALUES, not presence: a presence-only check would pass a transposition
	// that ships a postcode into the region slot of every record.
	for member, pair := range map[string]struct {
		got  *string
		want string
	}{
		"line1":       {full.Line1, "12 Main St"},
		"city":        {full.City, "Frankfurt"},
		"region":      {full.Region, "HE"},
		"postal_code": {full.PostalCode, "60311"},
		"country":     {full.Country, "DE"},
	} {
		got := "<nil>"
		if pair.got != nil {
			got = *pair.got
		}
		if got != pair.want {
			t.Errorf("%s = %q, want %q — a transposed member ships the wrong value into every record", member, got, pair.want)
		}
	}

	// A partial address still lands — dropping it would lose the only
	// location the incumbent had.
	partial := overlayAddress(map[string]any{"address": map[string]any{"city": "Berlin"}})
	if partial == nil || partial.City == nil || *partial.City != "Berlin" {
		t.Errorf("partial address = %+v, want the city carried", partial)
	}
	if partial != nil && partial.Line1 != nil {
		t.Errorf("absent members must stay nil, got line1 = %v", *partial.Line1)
	}
}

// A contact the incumbent holds no address for must read as absent, not as
// an address whose every member is empty.
func TestOverlayWirePersonOmitsAnEmptyAddress(t *testing.T) {
	rec := wireRecord(t, datasource.EntityPerson, map[string]any{
		"full_name": "Ada Overlay",
		"address":   map[string]any{"city": "  "},
	})
	person, err := overlayWirePerson(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.Address != nil {
		t.Errorf("Address = %+v, want nil when no member carries a value", person.Address)
	}
}

func TestOverlayWireTitlePicksThePerTypeDisplayField(t *testing.T) {
	for _, tc := range []struct {
		et     datasource.EntityType
		fields map[string]any
		want   string
	}{
		{datasource.EntityPerson, map[string]any{"first_name": "Ada", "last_name": "O"}, "Ada O"},
		{datasource.EntityOrganization, map[string]any{"display_name": "Acme GmbH"}, "Acme GmbH"},
		{datasource.EntityDeal, map[string]any{"name": "Renewal"}, "Renewal"},
		{datasource.EntityLead, map[string]any{"full_name": "Lea D"}, "Lea D"},
		{datasource.EntityActivity, map[string]any{"subject": "Kickoff"}, "Kickoff"},
	} {
		if got := overlayWireTitle(tc.et, tc.fields); got != tc.want {
			t.Errorf("title(%s) = %q, want %q", tc.et, got, tc.want)
		}
	}
}

// The child collection the wire reads is the one the mapping pipeline
// actually writes, seeded through the real HubSpot mapping and put through
// the same json round trip the mirror's jsonb column performs. Apply builds
// []map[string]any in-process and json.Unmarshal hands back []any; a reader
// tested only against the in-process shape would answer "" for every record
// that ever reached the database.
func TestOverlayChildReadersReadWhatTheMappingPipelineWrites(t *testing.T) {
	cases := []struct {
		incumbentClass string
		raw            map[string]any
		parent         string
		read           func(map[string]any) string
		want           string
	}{
		{
			incumbentClass: "contacts",
			raw:            map[string]any{"hs_object_id": "1", "email": "Ada@Example.TEST"},
			parent:         "person_email",
			read:           overlayPersonEmail,
			want:           "ada@example.test",
		},
		{
			incumbentClass: "companies",
			raw:            map[string]any{"hs_object_id": "2", "domain": "Acme.IO"},
			parent:         "organization_domain",
			read:           overlayOrgDomain,
			want:           "acme.io",
		},
	}
	for _, tc := range cases {
		m, ok := hubspot.Mapping(tc.incumbentClass)
		if !ok {
			t.Fatalf("Mapping(%s): want a declared mapping", tc.incumbentClass)
		}
		canonical, _, err := overlay.Apply(m, tc.raw)
		if err != nil {
			t.Fatalf("Apply(%s): %v", tc.incumbentClass, err)
		}
		if _, inProcess := canonical[tc.parent].([]map[string]any); !inProcess {
			t.Fatalf("%s in-process = %T, want the []map[string]any collection Apply builds", tc.parent, canonical[tc.parent])
		}
		encoded, err := json.Marshal(canonical)
		if err != nil {
			t.Fatalf("marshaling the canonical payload: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("decoding the canonical payload: %v", err)
		}
		if _, isAnySlice := decoded[tc.parent].([]any); !isAnySlice {
			t.Fatalf("%s decoded = %T, want the []any every JSON array decodes to", tc.parent, decoded[tc.parent])
		}
		if got := tc.read(decoded); got != tc.want {
			t.Errorf("reading %s from the decoded payload = %q, want %q", tc.parent, got, tc.want)
		}
		if got := tc.read(canonical); got != tc.want {
			t.Errorf("reading %s from the in-process payload = %q, want %q", tc.parent, got, tc.want)
		}
	}
}

// A mirror row written before child targets held collections carries a bare
// object, and the poller rewrites it only when the incumbent touches that
// record — never, for a record nobody edits again. Its email must still reach
// the wire.
func TestOverlayChildReadersStillReadTheSingleObjectShape(t *testing.T) {
	legacy := map[string]any{
		"person_email":        map[string]any{"email": "ada@example.test"},
		"organization_domain": map[string]any{"domain": "acme.io"},
	}
	if got := overlayPersonEmail(legacy); got != "ada@example.test" {
		t.Errorf("overlayPersonEmail = %q, want the address the pre-collection payload holds", got)
	}
	if got := overlayOrgDomain(legacy); got != "acme.io" {
		t.Errorf("overlayOrgDomain = %q, want the domain the pre-collection payload holds", got)
	}
	// A payload holding neither shape answers absent rather than erroring:
	// the true value always survives in raw.
	for name, fields := range map[string]map[string]any{
		"no key":                {},
		"a bare string":         {"person_email": "ada@example.test"},
		"rows that are strings": {"person_email": []any{"ada@example.test"}},
		"a row with no email":   {"person_email": []any{map[string]any{"email_type": "work"}}},
	} {
		if got := overlayPersonEmail(fields); got != "" {
			t.Errorf("%s: overlayPersonEmail = %q, want the empty answer", name, got)
		}
	}
}

// A child row's type and primary flag are the mapping's declaration, and the
// flip carries them onto the native row rather than assuming them — an
// imported contact whose mirrored address is a personal, non-primary one must
// not land as the work primary. A row carrying neither attribute reads as the
// single primary work address one mapped address means.
func TestFlipCarriesTheChildRowsDeclaredAttributes(t *testing.T) {
	m, ok := hubspot.Mapping("contacts")
	if !ok {
		t.Fatal("Mapping(contacts): want a declared mapping")
	}
	canonical, _, err := overlay.Apply(m, map[string]any{"hs_object_id": "1", "email": "Ada@Example.TEST"})
	if err != nil {
		t.Fatalf("Apply(contacts): %v", err)
	}
	emails := flipPersonEmails(canonical)
	if len(emails) != 1 || emails[0].Email != "ada@example.test" {
		t.Fatalf("emails = %v, want the one mapped address", emails)
	}
	if emails[0].EmailType != "work" || !emails[0].IsPrimary {
		t.Errorf("emails[0] = %+v, want the type and primary flag the mapping declared", emails[0])
	}

	declared := flipPersonEmails(map[string]any{"person_email": []any{map[string]any{
		"email": "ada@home.test", "email_type": "personal", "is_primary": false,
	}}})
	if len(declared) != 1 || declared[0].EmailType != "personal" || declared[0].IsPrimary {
		t.Errorf("emails = %+v, want the row's own personal, non-primary declaration", declared)
	}

	bare := flipPersonEmails(map[string]any{"person_email": []any{map[string]any{"email": "ada@example.test"}}})
	if len(bare) != 1 || bare[0].EmailType != "work" || !bare[0].IsPrimary {
		t.Errorf("emails = %+v on a row declaring no attributes, want the primary work address", bare)
	}
	if got := flipPersonEmails(map[string]any{}); got != nil {
		t.Errorf("emails = %+v for a record holding no address, want none", got)
	}

	domains := flipOrgDomains(map[string]any{"organization_domain": []any{map[string]any{
		"domain": "acme.io", "is_primary": false,
	}}})
	if len(domains) != 1 || domains[0].Domain != "acme.io" || domains[0].IsPrimary {
		t.Errorf("domains = %+v, want the row's own non-primary declaration", domains)
	}
}

// A mirrored contact that publishes no email address and no phone number is a
// contact a user cannot act on. The row identity is synthesized because the
// mirror holds none, and it is derived from the contact and the value so it
// stays fixed across reads rather than handing the client a fresh identity
// each time.
func TestOverlayWirePersonPublishesEmailsAndPhones(t *testing.T) {
	rec := wireRecord(t, datasource.EntityPerson, map[string]any{
		"full_name": "Ada Overlay",
		"person_email": []any{
			map[string]any{"email": "ada@example.de", "email_type": "work", "is_primary": true, "position": 0},
		},
		"person_phone": []any{
			map[string]any{"phone": "+4930111", "phone_type": "work", "is_primary": true, "position": 0},
			map[string]any{"phone": "+4917622", "phone_type": "mobile", "is_primary": false, "position": 1},
		},
	})
	person, err := overlayWirePerson(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.Emails == nil || len(*person.Emails) != 1 {
		t.Fatalf("Emails = %v, want the one mirrored address", person.Emails)
	}
	email := (*person.Emails)[0]
	if string(email.Email) != "ada@example.de" {
		t.Errorf("Email = %q, want the mirrored address", email.Email)
	}
	if email.EmailType != crmcontracts.PersonEmailEmailTypeWork || !email.IsPrimary {
		t.Errorf("email row = %+v, want the work/primary attributes the mapping declared", email)
	}
	if email.Source != "overlay" || email.CapturedBy == nil || *email.CapturedBy != "connector:overlay" {
		t.Error("a synthesized child row carries the same provenance stamp as its parent")
	}
	if person.Phones == nil || len(*person.Phones) != 2 {
		t.Fatalf("Phones = %v, want both the work and mobile numbers", person.Phones)
	}
	work, mobile := (*person.Phones)[0], (*person.Phones)[1]
	if work.PhoneType != crmcontracts.PersonPhonePhoneTypeWork || mobile.PhoneType != crmcontracts.PersonPhonePhoneTypeMobile {
		t.Errorf("phone types = %q then %q, want the order the mapping fixed", work.PhoneType, mobile.PhoneType)
	}
	if work.Phone != "+4930111" || mobile.Phone != "+4917622" {
		t.Errorf("phones = %q then %q, want each number on its own typed row", work.Phone, mobile.Phone)
	}
	if !work.IsPrimary || mobile.IsPrimary {
		t.Errorf("primary flags = %v then %v, want only the work number primary", work.IsPrimary, mobile.IsPrimary)
	}
	if work.Position != 0 || mobile.Position != 1 {
		t.Errorf("positions = %d then %d, want the declared order carried onto the wire", work.Position, mobile.Position)
	}
	if work.Id == mobile.Id || work.Id == (openapi_types.UUID{}) {
		t.Errorf("row ids = %v and %v, want two distinct non-zero identities", work.Id, mobile.Id)
	}
}

// The email and phone collections the wire publishes are the ones the mapping
// pipeline actually writes, seeded through the real HubSpot mapping and put
// through the same json round trip the mirror's jsonb column performs — the
// attribute keys compose reads are exactly the keys mapping_hs.go declares, and
// the mobile row's non-default type and primary flag are what proves it.
func TestOverlayWirePersonPublishesWhatTheMappingPipelineWrites(t *testing.T) {
	m, ok := hubspot.Mapping("contacts")
	if !ok {
		t.Fatal("Mapping(contacts): want a declared mapping")
	}
	canonical, _, err := overlay.Apply(m, map[string]any{
		"hs_object_id": "1", "email": "Ada@Example.DE",
		"phone": "+4930111", "mobilephone": "+4917622",
	})
	if err != nil {
		t.Fatalf("Apply(contacts): %v", err)
	}
	person, err := overlayWirePerson(wireCtx(), wireRecord(t, datasource.EntityPerson, canonical))
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.Emails == nil || len(*person.Emails) != 1 {
		t.Fatalf("Emails = %v, want the one mapped address", person.Emails)
	}
	if got := (*person.Emails)[0]; string(got.Email) != "ada@example.de" ||
		got.EmailType != crmcontracts.PersonEmailEmailTypeWork || !got.IsPrimary {
		t.Errorf("email = %+v, want the lowercased work primary address the mapping declares", got)
	}
	if person.Phones == nil || len(*person.Phones) != 2 {
		t.Fatalf("Phones = %v, want the work and mobile numbers the mapping declares", person.Phones)
	}
	mobile := (*person.Phones)[1]
	if mobile.Phone != "+4917622" {
		t.Errorf("phones[1].Phone = %q, want the mobilephone property", mobile.Phone)
	}
	// Non-default on both axes: the fallback type is work and the fallback
	// primary flag is false, so reading either attribute from the wrong key
	// would show up here and nowhere else.
	if mobile.PhoneType != crmcontracts.PersonPhonePhoneTypeMobile {
		t.Errorf("phones[1].PhoneType = %q, want the mobile type mapping_hs.go declares", mobile.PhoneType)
	}
	if !(*person.Phones)[0].IsPrimary || mobile.IsPrimary {
		t.Error("the work number is the declared primary and the mobile one is not")
	}
}

// A child row whose declared type is not one the contract knows must not ship
// an invalid enum: the value stays in raw and the row publishes the type one
// mapped address or number means.
func TestOverlayWirePersonFallsBackOnAnOffEnumChildType(t *testing.T) {
	rec := wireRecord(t, datasource.EntityPerson, map[string]any{
		"full_name":    "Ada Overlay",
		"person_email": []any{map[string]any{"email": "ada@example.de", "email_type": "billing"}},
		"person_phone": []any{map[string]any{"phone": "+4930111", "phone_type": "switchboard"}},
	})
	person, err := overlayWirePerson(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.Emails == nil || (*person.Emails)[0].EmailType != crmcontracts.PersonEmailEmailTypeWork {
		t.Errorf("Emails = %v, want the work fallback rather than an off-enum type", person.Emails)
	}
	if person.Phones == nil || (*person.Phones)[0].PhoneType != crmcontracts.PersonPhonePhoneTypeWork {
		t.Errorf("Phones = %v, want the work fallback rather than an off-enum type", person.Phones)
	}
	if person.Raw == nil {
		t.Fatal("the full canonical payload must ride raw")
	}
}

// A row carrying no number is skipped rather than published as a blank one:
// the incumbent leaves an unset property null, and the mapping still lands the
// row its ChildRow declares.
func TestOverlayWirePersonSkipsAChildRowWithNoValue(t *testing.T) {
	rec := wireRecord(t, datasource.EntityPerson, map[string]any{
		"full_name": "Ada Overlay",
		"person_phone": []any{
			map[string]any{"phone": nil, "phone_type": "work", "is_primary": true, "position": 0},
			map[string]any{"phone": "  ", "phone_type": "home", "is_primary": false, "position": 1},
			map[string]any{"phone": "+4917622", "phone_type": "mobile", "is_primary": false, "position": 2},
		},
	})
	person, err := overlayWirePerson(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.Phones == nil || len(*person.Phones) != 1 {
		t.Fatalf("Phones = %v, want only the row that carries a number", person.Phones)
	}
	if (*person.Phones)[0].Phone != "+4917622" {
		t.Errorf("Phones[0] = %+v, want the mobile number", (*person.Phones)[0])
	}
	bare, err := overlayWirePerson(wireCtx(), wireRecord(t, datasource.EntityPerson, map[string]any{"full_name": "Ada"}))
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if bare.Emails != nil || bare.Phones != nil {
		t.Errorf("Emails = %v, Phones = %v, want both absent when the mirror holds neither", bare.Emails, bare.Phones)
	}
}

// A child row's position decodes as float64 through the mirror's jsonb column
// and stays an int in-process; a reader that knew only one shape would order
// every stored record by zero.
func TestChildRowPositionReadsBothDecodedAndInProcessShapes(t *testing.T) {
	for name, row := range map[string]map[string]any{
		"decoded":    {"position": float64(2)},
		"in-process": {"position": 2},
	} {
		if got := childRowPosition(row); got != 2 {
			t.Errorf("%s: childRowPosition = %d, want 2", name, got)
		}
	}
	for name, row := range map[string]map[string]any{
		"absent":      {},
		"a string":    {"position": "2"},
		"fractional":  {"position": 1.5},
		"unbounded":   {"position": 1e19},
		"not a value": {"position": nil},
	} {
		if got := childRowPosition(row); got != 0 {
			t.Errorf("%s: childRowPosition = %d, want the collection's first slot", name, got)
		}
	}
}

// A churning row id would hand the SPA a fresh identity for the same value on
// every read; two values of one parent sharing an id would collapse them in
// any keyed render.
func TestOverlaySyntheticChildIDsAreStableAndDistinct(t *testing.T) {
	parent := openapi_types.UUID(ids.NewV7())
	first := overlaySyntheticID(parent, "ada@example.de")
	if again := overlaySyntheticID(parent, "ada@example.de"); first != again {
		t.Error("the same parent and value must always derive the same id")
	}
	if other := overlaySyntheticID(parent, "ada@example.com"); first == other {
		t.Error("two values of one parent must derive different ids")
	}
	if elsewhere := overlaySyntheticID(openapi_types.UUID(ids.NewV7()), "ada@example.de"); first == elsewhere {
		t.Error("the same value under different parents must derive different ids")
	}
}
