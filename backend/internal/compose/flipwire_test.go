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
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
		if pair.got == nil || *pair.got != pair.want {
			t.Errorf("%s = %v, want %q", field, pair.got, pair.want)
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
