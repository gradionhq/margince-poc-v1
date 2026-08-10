// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A signal reaches an organization two ways, and the account filter answers
// for both: the resolver stamps resolved_org_id on the item it attributed,
// and a signal created directly ABOUT the organization carries the
// (entity_type, entity_id) subject pair and no resolved_org_id at all.
// A deal-subject signal belongs to its deal and stays out of both arms.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

var signalReaderPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"organization":          {Create: true, Read: true},
		"signal":                {Create: true, Read: true},
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeAll,
}

func TestListSignalsByOrganizationCoversResolvedAndDirectSubjects(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, signalReaderPerms)
	store := signals.NewStore(e.Pool, nil)

	acme := e.SeedOrg(t, "Acme", &e.Rep1)
	other := e.SeedOrg(t, "Contoso", &e.Rep1)

	direct := SeedRow(t, owner, `INSERT INTO signal
		(id, workspace_id, kind, source_channel, entity_type, entity_id, resolution_state, severity, summary, detected_at, source, captured_by)
		VALUES ($1, $2, 'risk', 'derived', 'organization', '`+acme.String()+`', 'resolved', 'warn',
		        'Budget freeze mentioned on the call', now(), 'manual', 'human:x')`, e.WS)
	// The resolver's shape: the SUBJECT is a person, and the account it
	// belongs to is stamped on resolved_org_id. Matching on the subject pair
	// alone would miss it; matching on resolved_org_id alone would miss the
	// direct one above. The filter has to carry both arms.
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	resolved := SeedRow(t, owner, `INSERT INTO signal
		(id, workspace_id, kind, source_channel, entity_type, entity_id, resolved_org_id,
		 resolution_state, severity, summary, detected_at, source, captured_by)
		VALUES ($1, $2, 'buying_intent', 'web', 'person', '`+contact.String()+`', '`+acme.String()+`', 'resolved', 'warn',
		        'Pricing page visited five times', now(), 'manual', 'human:x')`, e.WS)
	// A signal about a different account must not leak into the filter.
	SeedRow(t, owner, `INSERT INTO signal
		(id, workspace_id, kind, source_channel, entity_type, entity_id, resolved_org_id,
		 resolution_state, severity, summary, detected_at, source, captured_by)
		VALUES ($1, $2, 'risk', 'web', 'organization', '`+other.String()+`', '`+other.String()+`', 'resolved', 'warn',
		        'Contoso churn risk', now(), 'manual', 'human:x')`, e.WS)

	got, _, err := store.ListSignals(ctx, signals.ListSignalsInput{OrganizationID: &acme})
	if err != nil {
		t.Fatalf("list signals for one account: %v", err)
	}
	found := map[ids.UUID]bool{}
	for _, sig := range got {
		found[ids.UUID(sig.Id)] = true
	}
	if !found[direct] {
		t.Error("a signal created directly about the organization is missing — it carries no resolved_org_id, so the subject pair is the only way to find it")
	}
	if !found[resolved] {
		t.Error("a resolver-attributed signal is missing from its own account's list")
	}
	if len(got) != 2 {
		t.Errorf("signals for the account = %d, want exactly the two about it", len(got))
	}
}
