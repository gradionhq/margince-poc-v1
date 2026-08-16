// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The three holes review found in #1373's first sweep, pinned as behaviour.
//
// They share one shape and it is worth naming, because it is the shape a
// call-site sweep is blind to: in all three the mutation does not probe the
// shared record AT ALL. A contract inherits its whole row scope from its
// anchor, an approval decision inherits it from the record the effect will
// write, and a revoke inherits it from the record the grant is about — so the
// probe that stands in front of each of them names a table the writer never
// mentions, and no amount of reading the writer would have found it.
//
// Written against behaviour rather than against the probes for the same reason:
// the fitness function cannot see any of these, and says so.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// shareRecord writes a grant through the real writer, as the record's owner.
func shareRecord(owner context.Context, t *testing.T, e *Env, recordType string, record, subject ids.UUID, access string) {
	t.Helper()
	if _, err := identity.NewService(e.Pool).CreateRecordGrant(owner, identity.CreateGrantInput{
		RecordType: recordType, RecordID: record,
		SubjectType: "user", SubjectID: subject, Access: access,
	}); err != nil {
		t.Fatalf("sharing the %s as %s → %v", recordType, access, err)
	}
}

// grantsAtTeamScope is a seat holding every listed object outright, bounded to
// one team — so a refusal below can only come from the row authority.
func grantsAtTeamScope(objects ...string) principal.Permissions {
	grants := make(map[string]principal.ObjectGrant, len(objects))
	for _, object := range objects {
		grants[object] = principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true}
	}
	return principal.Permissions{RoleKeys: []string{"custom"}, Objects: grants, RowScope: principal.RowScopeTeam}
}

// A contract owns no owner_id: it is visible, and used to be MUTABLE, through
// whichever record it hangs off. The visibility clause its module renders was
// the only gate in front of every patch, archive, status change, cancellation
// and renewal — so a `read` share of one deal carried write on all of its
// agreements.
func TestAReadShareOfADealCannotRewriteItsContracts(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	owner := e.As(e.Rep3, []ids.UUID{e.Team2}, grantsAtTeamScope("deal", "organization", "contract"))
	holder := e.As(e.Rep1, []ids.UUID{e.Team1}, grantsAtTeamScope("deal", "organization", "contract"))

	org := ids.NewV7()
	e.WsExec(t, `INSERT INTO organization (id, workspace_id, owner_id, display_name, source, captured_by)
		VALUES ($1, $2, $3, 'Anchor GmbH', 'manual', 'human:x')`, org, e.WS, e.Rep3)
	deal := ids.NewV7()
	e.WsExec(t, `INSERT INTO deal (id, workspace_id, owner_id, name, pipeline_id, stage_id, organization_id, source, captured_by)
		VALUES ($1, $2, $3, 'Anchored Deal', $4, $5, $6, 'manual', 'human:x')`,
		deal, e.WS, e.Rep3, pipeline, open, org)

	store := contracts.NewStore(e.DB())
	// Fixed, because nothing here is about WHEN: the term's dates never reach an
	// assertion, and a fixture reading the wall clock is one that can fail for a
	// reason its own name does not mention.
	starts := time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC)
	contract, err := store.CreateContract(owner, contracts.CreateContractInput{
		OrganizationID: ids.From[ids.OrganizationKind](org),
		DealID:         idPtr(ids.From[ids.DealKind](deal)),
		Title:          "Framework agreement",
		StartsOn:       &starts,
		ValueBasis:     "total",
		Source:         "manual",
	})
	if err != nil {
		t.Fatalf("the owner creates the contract → %v", err)
	}
	id := ids.From[ids.ContractKind](ids.UUID(contract.Id))
	retitle := func() error {
		title := "Rewritten by a reader"
		_, err := store.UpdateContract(holder, id, crmcontracts.UpdateContractRequest{Title: &title}, nil)
		return err
	}

	// Nothing shared: the contract is not there as far as this caller is
	// concerned, and that answer must not change.
	if err := retitle(); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("patching a contract on an unshared deal → %v, want not-found", err)
	}

	shareRecord(owner, t, e, "deal", deal, e.Rep1, "read")
	// The read share opens the agreement — that half is the feature.
	if _, err := store.GetContract(holder, id); err != nil {
		t.Fatalf("a read share of the deal does not open its contract: %v", err)
	}
	if err := retitle(); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("patching a contract under a read share of its deal → %v, want permission-denied", err)
	}
	if err := store.ArchiveContract(holder, id); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("archiving a contract under a read share of its deal → %v, want permission-denied", err)
	}

	shareRecord(owner, t, e, "deal", deal, e.Rep1, "write")
	if err := retitle(); err != nil {
		t.Fatalf("patching a contract under a write share of its deal → %v, want allowed", err)
	}
}

// Approving a staged change is how the change happens: the effect writes the
// record on the decider's say-so, and several effects run under a system
// principal, which every row probe waves through. So the DECISION is where the
// authority has to be asked for, and it was asking the visibility question — a
// `read` share let its holder green-light a write they could not perform.
//
// The module's own rule is that triage visibility and the decision gate are one
// predicate ("you see exactly what you could act on"), so the assertion is the
// module's existing one: the approval is invisible, and both Get and Decide
// answer not-found rather than 403.
func TestAReadShareOfARecordCannotDecideAChangeStagedAgainstIt(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.DB())
	owner := e.As(e.Rep3, []ids.UUID{e.Team2}, grantsAtTeamScope("person", "approval"))
	holder := e.As(e.Rep1, []ids.UUID{e.Team1}, grantsAtTeamScope("person", "approval"))

	person := ids.NewV7()
	e.WsExec(t, `INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, $3, 'Staged Subject', 'manual', 'human:x')`, person, e.WS, e.Rep3)

	staged, err := svc.Stage(e.AgentCtx(), approvals.StageInput{
		Kind: "archive_record", ProposedChange: json.RawMessage(`{}`),
		DiffHash: "h-" + ids.NewV7().String(), TargetType: "person",
		TargetID: person, Summary: "archive the shared contact",
	})
	if err != nil {
		t.Fatalf("staging against the shared person → %v", err)
	}

	shareRecord(owner, t, e, "person", person, e.Rep1, "read")
	assertCannotDecideStagedApproval(holder, t, svc,
		"a colleague holding only a read share of the target", staged)

	// The allow arm, on the same row and the same seat: only the access column
	// moved. Without it every assertion above would pass against a gate that
	// refused this seat for some other reason entirely.
	shareRecord(owner, t, e, "person", person, e.Rep1, "write")
	if _, err := svc.Get(holder, staged); err != nil {
		t.Fatalf("a write share does not open the staged change: %v", err)
	}
	if _, err := svc.Decide(holder, staged, false, strPtr("not now")); err != nil {
		t.Fatalf("deciding under a write share → %v, want allowed", err)
	}
}

// Revoking a share is administration OF the sharing, so it wants the authority
// asserting one wants. It used to want only visibility, which let anyone the
// record had ever been shared with — read-only — delete a colleague's `write`
// grant on it.
func TestAReadShareCannotRevokeSomebodyElsesShare(t *testing.T) {
	e := Setup(t)
	svc := identity.NewService(e.Pool)
	owner := e.As(e.Rep3, []ids.UUID{e.Team2}, grantsAtTeamScope("person"))
	holder := e.As(e.Rep1, []ids.UUID{e.Team1}, grantsAtTeamScope("person"))

	person := ids.NewV7()
	e.WsExec(t, `INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, $3, 'Twice Shared', 'manual', 'human:x')`, person, e.WS, e.Rep3)
	shareRecord(owner, t, e, "person", person, e.Rep2, "write")
	shareRecord(owner, t, e, "person", person, e.Rep1, "read")
	colleagues := grantIDsFor(t, e, person, e.Rep2)
	mine := grantIDsFor(t, e, person, e.Rep1)

	if err := svc.RevokeRecordGrant(holder, colleagues); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a read-share holder revoking a colleague's write share → %v, want permission-denied", err)
	}
	// Declining your OWN share is not authority over the record and stays
	// possible — it is the only way out from under a share nobody asked for.
	if err := svc.RevokeRecordGrant(holder, mine); err != nil {
		t.Fatalf("declining one's own share → %v, want allowed", err)
	}
	// And the owner, who does hold write authority, can still take the other
	// one away — so the refusal above is the rule and not a broken revoke.
	if err := svc.RevokeRecordGrant(owner, colleagues); err != nil {
		t.Fatalf("the owner revoking a share → %v, want allowed", err)
	}
}

// The seat the self-revocation arm exists for, and the one an object gate in
// front of it would have locked out: a READ-ONLY member holds no person:update
// at all, so if declining a share had to pass that check first, the person least
// able to do anything with the record would be the only one unable to give it
// back.
func TestAReadOnlySeatCanStillDeclineItsOwnShare(t *testing.T) {
	e := Setup(t)
	svc := identity.NewService(e.Pool)
	owner := e.As(e.Rep3, []ids.UUID{e.Team2}, grantsAtTeamScope("person"))
	readOnly := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"read_only"},
		Objects:  map[string]principal.ObjectGrant{"person": {Read: true}},
		RowScope: principal.RowScopeTeam,
	})

	person := ids.NewV7()
	e.WsExec(t, `INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
		VALUES ($1, $2, $3, 'Unwanted Share', 'manual', 'human:x')`, person, e.WS, e.Rep3)
	shareRecord(owner, t, e, "person", person, e.Rep1, "read")

	if err := svc.RevokeRecordGrant(readOnly, grantIDsFor(t, e, person, e.Rep1)); err != nil {
		t.Fatalf("a read-only seat declining its own share → %v, want allowed", err)
	}
}

// The partner row's own comment already stated this invariant — "promotion
// flips organization.classification — that is an org mutation, so the org's own
// write grant is required too" — and only the OBJECT half of it was enforced.
// The row half probed for sight, so a `read` share of a company let its holder
// reclassify it as a partner.
func TestAReadShareOfACompanyCannotMakeItAPartner(t *testing.T) {
	e := Setup(t)
	owner := e.As(e.Rep3, []ids.UUID{e.Team2}, grantsAtTeamScope("organization", "partner"))
	holder := e.As(e.Rep1, []ids.UUID{e.Team1}, grantsAtTeamScope("organization", "partner"))

	org := ids.NewV7()
	e.WsExec(t, `INSERT INTO organization (id, workspace_id, owner_id, display_name, source, captured_by)
		VALUES ($1, $2, $3, 'Reseller GmbH', 'manual', 'human:x')`, org, e.WS, e.Rep3)

	promote := func(as context.Context) error {
		_, err := people.NewStore(e.DB()).UpsertPartner(as, people.UpsertPartnerInput{
			OrganizationID: ids.From[ids.OrganizationKind](org), PartnerRole: "hosting",
		})
		return err
	}

	if err := promote(holder); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("promoting an unshared company → %v, want not-found", err)
	}
	shareRecord(owner, t, e, "organization", org, e.Rep1, "read")
	if err := promote(holder); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("promoting under a read share → %v, want permission-denied", err)
	}
	shareRecord(owner, t, e, "organization", org, e.Rep1, "write")
	if err := promote(holder); err != nil {
		t.Fatalf("promoting under a write share → %v, want allowed", err)
	}
}

func grantIDsFor(t *testing.T, e *Env, record, subject ids.UUID) ids.UUID {
	t.Helper()
	var id ids.UUID
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM record_grant WHERE record_id = $1 AND subject_id = $2`,
			record, subject).Scan(&id)
	}); err != nil {
		t.Fatalf("reading back the grant: %v", err)
	}
	return id
}
