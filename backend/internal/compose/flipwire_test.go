// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The flip's wire-shaping, operator resolution, and field mapping,
// unit-tested: these decide what an operator is TOLD about a cutover —
// the disclosed-lossy notice, whether the emergency path is offered at
// all, who inherits records the incumbent left unowned — and which
// native column each incumbent value lands in. Getting them wrong is a
// silent misrepresentation rather than a failure.

import (
	"context"
	"strings"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

func TestBlockingContains(t *testing.T) {
	blocking := []crmcontracts.OverlayFlipPreflightBlocking{
		crmcontracts.ForceFreshIncomplete, crmcontracts.ExportMissing,
	}
	if !blockingContains(blocking, crmcontracts.ExportMissing) {
		t.Error("a present reason was not found; the emergency block and the export gate both branch on this")
	}
	if blockingContains(blocking, crmcontracts.IncumbentUnreachable) {
		t.Error("an absent reason was reported present — the emergency cutover would be offered while the incumbent is reachable")
	}
	if blockingContains(nil, crmcontracts.ExportMissing) {
		t.Error("a green verdict must contain no reason at all")
	}
}

func TestEmergencyDisclosureIsAlwaysLossyLabelled(t *testing.T) {
	synced := emergencyDisclosure(time.Now().Add(-2 * time.Hour))
	if synced.UnverifiableParityNotice == "" {
		t.Fatal("an emergency cutover must always carry the unverifiable-parity notice — it is the disclosure OVA-AC-6(b) requires")
	}
	if synced.LastSyncedAt == nil || synced.StalenessSeconds == nil || *synced.StalenessSeconds < 7100 {
		t.Errorf("disclosure = %+v, want ~2h of staleness stated", synced)
	}

	// A mirror that never synced states the notice and NO age, rather
	// than a zero that would read as "synced just now".
	never := emergencyDisclosure(time.Time{})
	if never.UnverifiableParityNotice == "" {
		t.Error("the notice is unconditional")
	}
	if never.LastSyncedAt != nil || never.StalenessSeconds != nil {
		t.Errorf("disclosure = %+v, want no fabricated age", never)
	}
}

func TestWireEmergencyOffersOnlyWhatCanBeCutOverFrom(t *testing.T) {
	withMirror := wireEmergency(overlay.FlipChecks{MirrorRows: 42, LastSyncedAt: time.Now().Add(-time.Hour)})
	if !withMirror.Available {
		t.Error("a populated mirror is exactly what the emergency path cuts over from")
	}
	if withMirror.LastSyncedAt == nil || withMirror.UnverifiableParityNotice == "" {
		t.Errorf("emergency block = %+v, want the staleness and the notice", withMirror)
	}

	// Nothing mirrored: the option is surfaced but honestly unavailable —
	// there is no snapshot to rebuild an estate from.
	empty := wireEmergency(overlay.FlipChecks{})
	if empty.Available {
		t.Error("an empty mirror must not advertise an emergency cutover")
	}
	if empty.UnverifiableParityNotice == "" {
		t.Error("the notice still explains what the path would cost")
	}
}

func TestFlipOperatorRequiresAHuman(t *testing.T) {
	user := ids.NewV7()
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
	})
	operator, err := flipOperator(ctx)
	if err != nil {
		t.Fatalf("flipOperator: %v", err)
	}
	if operator == nil || operator.UUID != user {
		t.Errorf("operator = %v, want the acting user — unmapped-owner records are imported under them", operator)
	}

	if _, err := flipOperator(context.Background()); err == nil {
		t.Error("no actor at all must be refused, not silently imported ownerless")
	}

	// A principal with no user id (a system/service actor) cannot inherit
	// records: falling back to a null owner is the visibility widening
	// the operator fallback exists to prevent.
	system := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalSystem, ID: "system",
	})
	if _, err := flipOperator(system); err == nil {
		t.Error("an actor with no user id must be refused")
	}
}

func TestBundleSourceImportsTheEstateInDependencyOrder(t *testing.T) {
	order := bundleFlipSource{}.Objects()
	if len(order) != len(flipImportOrder) {
		t.Fatalf("bundle order = %v, want the same classes the live flip imports", order)
	}
	position := map[string]int{}
	for i, object := range order {
		position[object] = i
	}
	// Parents before dependents: an organization must exist before the
	// person or deal that references it, and activities land last so
	// every link target is already there.
	if position[flipObjectOrganization] > position[flipObjectPerson] ||
		position[flipObjectOrganization] > position[flipObjectDeal] {
		t.Errorf("order %v puts organizations after their dependents", order)
	}
	if position[flipObjectActivity] != len(order)-1 {
		t.Errorf("order %v does not import activities last", order)
	}
}

func TestFlipAddressOnlyBuildsAnAddressThatSaysSomething(t *testing.T) {
	// The incumbent's address transform emits a property map; a row with
	// no address at all, or one whose fields are all empty, must yield
	// nil rather than an Address of blanks the native row would store.
	for name, fields := range map[string]map[string]any{
		"no address key":     {"display_name": "Acme"},
		"address not a map":  {"address": "12 Main St"},
		"empty address map":  {"address": map[string]any{}},
		"only unknown parts": {"address": map[string]any{"floor": "3"}},
	} {
		if got := flipAddress(fields); got != nil {
			t.Errorf("%s: flipAddress = %+v, want nil", name, got)
		}
	}

	full := flipAddress(map[string]any{"address": map[string]any{
		"address": "12 Main St", "city": "Frankfurt", "state": "HE",
		"zip": "60311", "country": "DE",
	}})
	if full == nil {
		t.Fatal("a populated incumbent address must map to a native Address")
	}
	// VALUES, not presence: this function's whole job is renaming the
	// incumbent's keys onto the native ones (address→Line1, state→Region,
	// zip→PostalCode), so a presence-only check would pass a
	// transposition that ships a wrong postcode into every flipped row.
	for field, pair := range map[string]struct {
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
			t.Errorf("%s = %q, want %q — a transposed key ships the wrong value into every flipped row", field, got, pair.want)
		}
	}

	// A partial address still lands — dropping it would lose the only
	// location the incumbent had.
	partial := flipAddress(map[string]any{"address": map[string]any{"city": "Berlin"}})
	if partial == nil || partial.City == nil || *partial.City != "Berlin" {
		t.Errorf("partial address = %+v, want the city carried", partial)
	}
	if partial != nil && partial.Line1 != nil {
		t.Errorf("absent parts must stay nil, got line1 = %v", *partial.Line1)
	}
}

func TestExportCutoffNeverFallsBackToAMeaninglessZero(t *testing.T) {
	sweep := time.Now().Add(-time.Hour)
	rows := time.Now().Add(-10 * time.Minute)

	// An empty estate stamps no row watermark. Without the sweep the
	// cutoff would be the zero time and ANY export ever written — one
	// predating the incumbent connection entirely — would clear the gate.
	if got := exportCutoff(overlay.FlipChecks{LastSweepAt: sweep}); !got.Equal(sweep) {
		t.Errorf("cutoff on an empty mirror = %v, want the sweep at %v", got, sweep)
	}
	// With rows, the freshest change wins: a bundle taken before the last
	// mirrored edit no longer describes the estate the flip migrates.
	if got := exportCutoff(overlay.FlipChecks{LastSweepAt: sweep, LastSyncedAt: rows}); !got.Equal(rows) {
		t.Errorf("cutoff = %v, want the newer row watermark %v", got, rows)
	}
	// And a sweep that ran after the last row change still advances it.
	if got := exportCutoff(overlay.FlipChecks{LastSweepAt: rows, LastSyncedAt: sweep}); !got.Equal(rows) {
		t.Errorf("cutoff = %v, want the newer sweep %v", got, rows)
	}
}

func TestExportGateProvesNothingWithoutAWatermark(t *testing.T) {
	// A workspace that has neither mirrored a row nor completed a sweep
	// offers no instant to compare an export against. The cutoff is zero,
	// and the gate must read that as "no export proven" — comparing
	// against it would clear on any export in the workspace's history.
	if got := exportCutoff(overlay.FlipChecks{}); !got.IsZero() {
		t.Errorf("cutoff = %v, want the zero time on a workspace with no watermark at all", got)
	}
	exported, err := (&flipRunner{}).exportSince(t.Context(), time.Time{})
	if err != nil {
		t.Fatalf("exportSince: %v", err)
	}
	if exported {
		t.Error("a zero cutoff reported an export as proven; every historical export would clear the gate")
	}
}

func TestTheFlipStampsProvenanceInsideTheReservedNamespace(t *testing.T) {
	// The resume repair recognizes its own records by this prefix, and
	// only the prefix makes that safe — every client create wire refuses
	// it, so a row carrying it cannot have been planted.
	w := &flipWriters{incumbent: "hubspot"}
	stamp := w.provenance(flipObjectPerson, "p-1")
	if !provenance.ReservedSourceSystem(stamp) {
		t.Fatalf("provenance = %q, want the reserved prefix; without it a planted row would be adopted as the importer's own", stamp)
	}
	if stamp != provenance.ReservedSourceSystemPrefix+"hubspot:person:p-1" {
		t.Errorf("provenance = %q, want incumbent:object:external_id inside the namespace", stamp)
	}
	// The empty external id is the LIKE prefix the repair scans on, so it
	// must be a strict prefix of a real row's stamp — otherwise the scan
	// matches nothing and the repair silently adopts nobody.
	if prefix := w.provenance(flipObjectPerson, ""); !strings.HasPrefix(stamp, prefix) || prefix == stamp {
		t.Errorf("scan prefix %q is not a strict prefix of %q", prefix, stamp)
	}
	// Distinct classes never share a prefix, or the repair would bind an
	// organization's external id to a person.
	if strings.HasPrefix(w.provenance(flipObjectDeal, "x"), w.provenance(flipObjectPerson, "")) {
		t.Error("a deal's provenance matched the person scan prefix")
	}
}

func TestReconcileRefusesAnObjectOutsideTheAllowlist(t *testing.T) {
	// The class is interpolated into the scan's FROM clause, so the
	// allowlist — not the caller — is what keeps a table name out of it.
	if _, err := (&flipWriters{incumbent: "hubspot"}).orphanedIdentities(t.Context(), "person; DROP TABLE person"); err == nil {
		t.Fatal("an unlisted object must be refused before any query is built")
	}
}
