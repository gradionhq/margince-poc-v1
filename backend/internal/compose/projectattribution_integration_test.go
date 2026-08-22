// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The project attribution ladder's uncertain rung end to end over a real
// migrated Postgres: a captured message no deterministic rung can file is
// OFFERED to a human rather than filed, the confirm files it through the
// relink path with its retention stamp, and the decline leaves it unfiled
// and is not asked again — for that message, or for its thread.
//
// Every fixture is written by the thing that writes it in production: the
// account, the contact and their employment through the people store, the
// projects through the deals store, the message through the composed capture
// sink, the decision through the approvals service the HTTP door decides on.

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// attributionCounterparty is who writes the mail the rung files: a contact
// employed at the account whose projects the rung reads.
const attributionCounterparty = "alice@acme.example"

// attributionAccount is the account, its contact and the sink, seeded the way
// production seeds them.
type attributionAccount struct {
	e     *integration.Env
	orgID ids.UUID
	sink  *capture.Sink
	// captureCtx is the connector principal a mail sync acts as: the mailbox
	// owner's authority, borrowed.
	captureCtx context.Context
}

func seedAttributionAccount(t *testing.T, e *integration.Env) attributionAccount {
	t.Helper()
	orgID := e.SeedOrg(t, "Acme", nil)
	person, err := e.People.CreatePerson(e.Admin(), people.CreatePersonInput{
		FullName: "Alice Example", Source: "manual",
		Emails: []people.PersonEmailInput{{Email: attributionCounterparty, EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seeding the contact: %v", err)
	}
	personID := ids.From[ids.PersonKind](ids.UUID(person.Id))
	employer := ids.From[ids.OrganizationKind](orgID)
	if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
		Kind: "employment", PersonID: &personID, OrganizationID: &employer, Source: "manual",
	}); err != nil {
		t.Fatalf("seeding the employment: %v", err)
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:gmail",
		UserID: e.Rep1, OnBehalfOf: e.Rep1,
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"activity":     {Create: true, Read: true, Update: true},
				"person":       {Create: true, Read: true},
				"organization": {Create: true, Read: true},
				"project":      {Read: true},
				"deal":         {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return attributionAccount{e: e, orgID: orgID, sink: newCaptureSink(e.Pool, CaptureConfig{}), captureCtx: ctx}
}

// project opens one live project on the account through the store that owns
// the table.
func (a attributionAccount) project(t *testing.T, name, key string) ids.UUID {
	t.Helper()
	created, err := a.e.Deals.CreateProject(a.e.Admin(), deals.CreateProjectInput{
		Name: name, Key: &key, OrganizationID: ids.From[ids.OrganizationKind](a.orgID), Source: "manual",
	})
	if err != nil {
		t.Fatalf("creating project %q: %v", name, err)
	}
	return ids.UUID(created.Id)
}

// capture lands one inbound mail from the contact through the composed sink,
// with the ladder running post-commit exactly as a sync would run it, and
// answers the activity it became.
func (a attributionAccount) capture(t *testing.T, sourceID, inReplyTo, subject string) ids.UUID {
	t.Helper()
	threadKey := sourceID
	if inReplyTo != "" {
		threadKey = inReplyTo
	}
	ref, err := a.sink.Upsert(a.captureCtx, connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: "gmail", SourceID: sourceID},
		Fields: capture.ActivityFields{
			Kind: "email", Subject: subject, Body: "hello from Acme", Direction: connector.DirectionInbound,
		},
		Source:     "gmail:" + sourceID,
		CapturedBy: "connector:gmail",
		Raw:        []byte("From: " + attributionCounterparty + "\r\n\r\nhello"),
		ThreadKey:  threadKey,
		Counterparty: connector.Counterparty{
			Email: attributionCounterparty, Domain: "acme.example", Direction: connector.DirectionInbound,
		},
	})
	if err != nil {
		t.Fatalf("capturing %s: %v", sourceID, err)
	}
	return ref.ID
}

// stagedAttribution answers the one pending project_attribution offer for the
// activity, or the zero id.
func stagedAttribution(t *testing.T, e *integration.Env, activityID ids.UUID) ids.UUID {
	t.Helper()
	var id ids.UUID
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT coalesce((SELECT id FROM approval
			                  WHERE kind = $1 AND target_entity_id = $2 AND status = 'pending'),
			                 '00000000-0000-0000-0000-000000000000'::uuid)`,
			approvals.KindProjectAttribution, activityID).Scan(&id)
	})
	if err != nil {
		t.Fatalf("reading the staged offer: %v", err)
	}
	return id
}

// filingOf is everything a filing leaves behind: the link, the retention
// class and its evidence, and the candidate's status.
type filingOf struct {
	projectLinks    int
	retentionClass  *string
	linkedEvidence  int
	candidateStatus *string
	candidateMethod *string
}

func readFiling(t *testing.T, e *integration.Env, activityID ids.UUID) filingOf {
	t.Helper()
	var got filingOf
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT (SELECT count(*) FROM activity_link WHERE activity_id = a.id AND entity_type = 'project'),
			       a.retention_class,
			       (SELECT count(*) FROM activity_retention_evidence ev
			         WHERE ev.activity_id = a.id AND ev.basis = 'project_linked'),
			       (SELECT c.status FROM project_link_candidate c WHERE c.activity_id = a.id
			         ORDER BY c.created_at DESC LIMIT 1),
			       (SELECT c.method FROM project_link_candidate c WHERE c.activity_id = a.id
			         ORDER BY c.created_at DESC LIMIT 1)
			  FROM activity a WHERE a.id = $1`, activityID).
			Scan(&got.projectLinks, &got.retentionClass, &got.linkedEvidence, &got.candidateStatus, &got.candidateMethod)
	})
	if err != nil {
		t.Fatalf("reading the filing: %v", err)
	}
	return got
}

// mustBeUnfiled is the rung's first obligation: it asks, it never files.
func mustBeUnfiled(t *testing.T, got filingOf, when string) {
	t.Helper()
	if got.projectLinks != 0 {
		t.Fatalf("%s: %d project links — the uncertain rung must never write a link itself", when, got.projectLinks)
	}
	if got.retentionClass != nil || got.linkedEvidence != 0 {
		t.Fatalf("%s: retention class %v with %d project_linked evidence rows on an unfiled message — only a link qualifies correspondence",
			when, got.retentionClass, got.linkedEvidence)
	}
}

// decideAttribution decides the offer as a real app_user holding the grant
// the kind demands, on the service the HTTP door decides through.
func decideAttribution(t *testing.T, e *integration.Env, approvalID ids.UUID, approve bool) {
	t.Helper()
	svc := approvalsServiceWithEffects(e.Pool)
	decider := e.As(e.Rep1, nil, integration.AdminPerms)
	if _, err := svc.Decide(decider, ids.From[ids.ApprovalKind](approvalID), approve, nil); err != nil {
		t.Fatalf("deciding the attribution offer (approve=%v): %v", approve, err)
	}
}

// awaitingDecision reads the project page's coverage number for the project,
// through the facts the page reads.
func awaitingDecision(t *testing.T, e *integration.Env, projectID ids.UUID) (awaiting, attributed int) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		facts, err := e.Activities.ProjectActivityFactsTx(e.Admin(), tx, ids.From[ids.ProjectKind](projectID))
		if err != nil {
			return err
		}
		awaiting, attributed = facts.AwaitingDecision, facts.Attributed
		return nil
	})
	if err != nil {
		t.Fatalf("reading the project's coverage: %v", err)
	}
	return awaiting, attributed
}

// The sole-live-project case, through to a confirm: the message is offered,
// not filed; confirming files it with the stamp the relink path writes, and
// the candidate and the coverage number say so.
func TestUncertainRungOffersTheSoleLiveProjectAndConfirmFilesIt(t *testing.T) {
	e := integration.Setup(t)
	account := seedAttributionAccount(t, e)
	erp := account.project(t, "ERP replacement", "ERP")

	activityID := account.capture(t, "t2-sole@acme.example", "", "weekly status")

	approvalID := stagedAttribution(t, e, activityID)
	if approvalID.IsZero() {
		t.Fatal("a message reaching an account with one live project staged no project_attribution offer")
	}
	before := readFiling(t, e, activityID)
	mustBeUnfiled(t, before, "after capture")
	if before.candidateStatus == nil || *before.candidateStatus != capture.CandidateStatusPending {
		t.Fatalf("candidate status = %v, want pending", before.candidateStatus)
	}
	if *before.candidateMethod != capture.MethodSoleLiveProject {
		t.Fatalf("candidate method = %q, want %s", *before.candidateMethod, capture.MethodSoleLiveProject)
	}
	if awaiting, attributed := awaitingDecision(t, e, erp); awaiting != 1 || attributed != 0 {
		t.Fatalf("coverage before the decision = awaiting %d / attributed %d, want 1 / 0", awaiting, attributed)
	}

	decideAttribution(t, e, approvalID, true)

	after := readFiling(t, e, activityID)
	if after.projectLinks != 1 {
		t.Fatalf("%d project links after confirm, want 1", after.projectLinks)
	}
	if after.retentionClass == nil || *after.retentionClass != "commercial_correspondence" || after.linkedEvidence != 1 {
		t.Fatalf("confirm filed the message without its stamp: class %v, %d project_linked evidence rows — a human-confirmed filing is a Handelsbrief like any other",
			after.retentionClass, after.linkedEvidence)
	}
	if *after.candidateStatus != capture.CandidateStatusConfirmed {
		t.Fatalf("candidate status after confirm = %q, want confirmed", *after.candidateStatus)
	}
	// The relink is the HUMAN's write, in their name: the confirm runs as the
	// decider, so the row-level write check and the destination probe apply
	// exactly as they would to a typed relink.
	if n := countIn(t, e, `
		SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = 'activity_relink'
		   AND actor_id = $2`, activityID, "human:"+e.Rep1.String()); n != 1 {
		t.Fatalf("%d activity_relink audit rows by the deciding human, want 1", n)
	}
	if awaiting, attributed := awaitingDecision(t, e, erp); awaiting != 0 || attributed != 1 {
		t.Fatalf("coverage after confirm = awaiting %d / attributed %d, want 0 / 1", awaiting, attributed)
	}
}

// The decline: nothing is written, the candidate records the refusal, and the
// question is not asked again — neither for this message (the engine's
// memory) nor for a reply in the same conversation (the ladder's).
func TestDecliningTheOfferLeavesTheMessageUnfiledAndIsNotAskedAgain(t *testing.T) {
	e := integration.Setup(t)
	account := seedAttributionAccount(t, e)
	erp := account.project(t, "ERP replacement", "ERP")

	activityID := account.capture(t, "t2-decline@acme.example", "", "weekly status")
	approvalID := stagedAttribution(t, e, activityID)
	if approvalID.IsZero() {
		t.Fatal("the fixture staged no offer — the decline below would prove nothing")
	}

	decideAttribution(t, e, approvalID, false)

	got := readFiling(t, e, activityID)
	mustBeUnfiled(t, got, "after decline")
	if *got.candidateStatus != capture.CandidateStatusRejected {
		t.Fatalf("candidate status after decline = %q, want rejected", *got.candidateStatus)
	}

	// The same pairing, offered again by a re-run of the rung: the engine
	// remembers the refusal and stages nothing.
	proposer := projectCandidateProposer{svc: approvals.NewService(InstallationDB(e.Pool))}
	_, staged, err := proposer.ProposeProjectCandidate(account.captureCtx, capture.ProjectCandidate{
		ActivityID: ids.From[ids.ActivityKind](activityID),
		Project:    capture.LiveProject{ID: erp, Name: "ERP replacement", Key: "ERP"},
		Method:     capture.MethodSoleLiveProject, Confidence: 1, Subject: "weekly status",
	})
	if err != nil {
		t.Fatalf("re-offering the refused pairing: %v", err)
	}
	if staged {
		t.Fatal("the refused pairing was staged again — StageUnlessDeclined must remember the human's no")
	}

	// A reply in the same conversation: a new activity the engine has no memory
	// of, which the ladder itself must recognize as already answered.
	replyID := account.capture(t, "t2-decline-reply@acme.example", "t2-decline@acme.example", "Re: weekly status")
	if offer := stagedAttribution(t, e, replyID); !offer.IsZero() {
		t.Fatalf("the reply staged offer %s — a refusal on one message is a refusal on its thread", offer)
	}
	mustBeUnfiled(t, readFiling(t, e, replyID), "the reply")
	if n := countIn(t, e, `SELECT count(*) FROM approval WHERE kind = $1`, approvals.KindProjectAttribution); n != 1 {
		t.Fatalf("%d project_attribution offers in total, want the one that was declined", n)
	}
}

// Several live projects and nothing to rank them by: the rung asks nothing.
// A question between two candidates a reviewer would have to research is not
// a question the inbox should carry.
func TestUncertainRungAsksNothingBetweenSeveralProjectsWithoutEmbeddings(t *testing.T) {
	e := integration.Setup(t)
	account := seedAttributionAccount(t, e)
	account.project(t, "ERP replacement", "ERP")
	account.project(t, "CRM rollout", "CRM")

	activityID := account.capture(t, "t2-two@acme.example", "", "weekly status")

	if offer := stagedAttribution(t, e, activityID); !offer.IsZero() {
		t.Fatalf("two live projects with no embeddings staged offer %s, want nothing", offer)
	}
	if n := countIn(t, e, `SELECT count(*) FROM project_link_candidate WHERE activity_id = $1`, activityID); n != 0 {
		t.Fatalf("%d candidate rows with nothing to decide between, want 0", n)
	}
	mustBeUnfiled(t, readFiling(t, e, activityID), "after capture")
}

// A message a human filed elsewhere while the offer waited is not re-filed by
// the confirm: the human's filing stands, nothing is overwritten, and the
// candidate closes.
func TestConfirmStandsDownWhenAHumanFiledTheMessageElsewhere(t *testing.T) {
	e := integration.Setup(t)
	account := seedAttributionAccount(t, e)
	account.project(t, "ERP replacement", "ERP")
	activityID := account.capture(t, "t2-moved@acme.example", "", "weekly status")
	approvalID := stagedAttribution(t, e, activityID)
	if approvalID.IsZero() {
		t.Fatal("the fixture staged no offer")
	}
	// A second project, opened after the offer, and the human files the
	// message there by hand through the relink door.
	crm := account.project(t, "CRM rollout", "CRM")
	if _, err := e.Activities.RelinkActivity(e.Admin(), ids.From[ids.ActivityKind](activityID),
		activities.RelinkActivityInput{EntityType: string(datasource.EntityProject), EntityID: crm}); err != nil {
		t.Fatalf("filing the message by hand: %v", err)
	}

	decideAttribution(t, e, approvalID, true)

	var filedUnder ids.UUID
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT project_id FROM activity_link WHERE activity_id = $1 AND entity_type = 'project'`, activityID).Scan(&filedUnder)
	})
	if err != nil {
		t.Fatalf("reading the filing: %v", err)
	}
	if filedUnder != crm {
		t.Fatalf("the confirm moved the message to %s over the human's filing under %s", filedUnder, crm)
	}
	got := readFiling(t, e, activityID)
	if *got.candidateStatus != capture.CandidateStatusRejected {
		t.Fatalf("candidate status = %q after the human filed elsewhere, want rejected", *got.candidateStatus)
	}
}

// stageSoleOffer seeds one account with one live project and captures one
// message, answering the offer the rung staged for it.
func stageSoleOffer(t *testing.T, e *integration.Env, sourceID string) (attributionAccount, ids.UUID, ids.UUID, ids.UUID) {
	t.Helper()
	account := seedAttributionAccount(t, e)
	erp := account.project(t, "ERP replacement", "ERP")
	activityID := account.capture(t, sourceID, "", "weekly status")
	approvalID := stagedAttribution(t, e, activityID)
	if approvalID.IsZero() {
		t.Fatal("the fixture staged no offer — the refusal below would prove nothing")
	}
	return account, erp, activityID, approvalID
}

// mustRefuseDecision asserts the decider can neither open nor decide the
// card, with the existence-hiding answer, and that nothing changed.
func mustRefuseDecision(decider context.Context, t *testing.T, e *integration.Env, approvalID, activityID ids.UUID, who string) {
	t.Helper()
	svc := approvalsServiceWithEffects(e.Pool)
	id := ids.From[ids.ApprovalKind](approvalID)
	if _, err := svc.Get(decider, id); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("%s could open the card (err=%v), want not-found — what they cannot act on they must not see", who, err)
	}
	if _, err := svc.Decide(decider, id, true, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("%s decided the card (err=%v), want not-found", who, err)
	}
	if got := stagedAttribution(t, e, activityID); got != approvalID {
		t.Fatalf("the offer is no longer pending after a refused decision (now %s)", got)
	}
	mustBeUnfiled(t, readFiling(t, e, activityID), "after the refused decision")
}

// Deciding the card is asserting a link to the project, so it takes the
// project read grant and the project's row scope — activity.update alone is
// not enough. Without this, someone who may edit messages but may not open
// projects could plant a reference to one they were never shown.
func TestADeciderWithoutTheProjectGrantCannotSeeOrDecideTheOffer(t *testing.T) {
	e := integration.Setup(t)
	_, _, activityID, approvalID := stageSoleOffer(t, e, "t2-noproject@acme.example")

	perms := principal.Permissions{
		RoleKeys: integration.AdminPerms.RoleKeys,
		Objects:  maps.Clone(integration.AdminPerms.Objects),
		RowScope: principal.RowScopeAll,
	}
	delete(perms.Objects, "project")
	if !perms.Allows("activity", principal.ActionUpdate) {
		t.Fatal("the fixture holds no activity.update — the refusal below would not be about the project")
	}
	mustRefuseDecision(e.As(e.Rep1, nil, perms), t, e, approvalID, activityID, "a decider without project.read")
}

// A colleague who may READ the message but not write it — customer
// correspondence is workspace-readable, the write arm stays the owner's team's
// — may open the card, and releasing it must still fail at the relink's own
// write check, the one a typed relink takes. The approval then stays approved
// and UNCONSUMED: the redemption rolled back with the write, nothing was
// filed, and the candidate still waits.
func TestADeciderWithReadOnlyAuthorityOverTheMessageCannotConfirmIt(t *testing.T) {
	e := integration.Setup(t)
	_, _, activityID, approvalID := stageSoleOffer(t, e, "t2-otherteam@acme.example")

	perms := principal.Permissions{
		RoleKeys: integration.AdminPerms.RoleKeys,
		Objects:  maps.Clone(integration.AdminPerms.Objects),
		RowScope: principal.RowScopeTeam,
	}
	decider := e.As(e.Rep3, []ids.UUID{e.Team2}, perms)
	svc := approvalsServiceWithEffects(e.Pool)
	_, err := svc.Decide(decider, ids.From[ids.ApprovalKind](approvalID), true, nil)
	if err == nil {
		t.Fatal("a team-scoped colleague released the filing of a message they may not write")
	}
	if !errors.Is(err, apperrors.ErrNotFound) && !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("the refusal is %v, want the relink's own not-found or permission-denied", err)
	}
	mustBeUnfiled(t, readFiling(t, e, activityID), "after the refused confirm")
	if n := countIn(t, e, `SELECT count(*) FROM approval WHERE id = $1 AND consumed_at IS NOT NULL`, approvalID); n != 0 {
		t.Fatal("the approval was consumed by an effect that wrote nothing — the redemption must roll back with the write")
	}
	if got := readFiling(t, e, activityID); *got.candidateStatus != capture.CandidateStatusPending {
		t.Fatalf("candidate status = %q after a refused confirm, want pending", *got.candidateStatus)
	}
}

// An offer nobody answers: the clock expires it, the candidate says so, the
// coverage number drops, and the SAME pairing may be asked again on the next
// capture in the conversation — an unanswered question is not a refused one.
func TestAnExpiredOfferFreesTheCandidateAndMayBeAskedAgain(t *testing.T) {
	e := integration.Setup(t)
	account, erp, activityID, approvalID := stageSoleOffer(t, e, "t2-expire@acme.example")
	if awaiting, _ := awaitingDecision(t, e, erp); awaiting != 1 {
		t.Fatalf("awaiting_decision before expiry = %d, want 1", awaiting)
	}
	// Backdated rather than waited for, as approvalexpiry_integration_test.go
	// does: the clock is the predicate under test, and the owner connection is
	// the only path that may move a window after the fact.
	e.WsExec(t, `UPDATE approval SET expires_at = now() - $2::interval WHERE id = $1`, approvalID, time.Hour.String())

	expired, err := expiringApprovalsService(e.Pool).ExpireDue(expiryCtx(e))
	if err != nil {
		t.Fatalf("ExpireDue: %v", err)
	}
	if len(expired) != 1 || expired[0].ID.UUID != approvalID {
		t.Fatalf("ExpireDue swept %v, want exactly the offer", expired)
	}
	got := readFiling(t, e, activityID)
	mustBeUnfiled(t, got, "after expiry")
	if got.candidateStatus == nil || *got.candidateStatus != capture.CandidateStatusExpired {
		t.Fatalf("candidate status after expiry = %v, want expired — a pending row nobody can answer would count forever", got.candidateStatus)
	}
	if awaiting, _ := awaitingDecision(t, e, erp); awaiting != 0 {
		t.Fatalf("awaiting_decision after expiry = %d, want 0", awaiting)
	}

	// The next message in the conversation asks again: no refusal stands.
	replyID := account.capture(t, "t2-expire-reply@acme.example", "t2-expire@acme.example", "Re: weekly status")
	if offer := stagedAttribution(t, e, replyID); offer.IsZero() {
		t.Fatal("a reply after an EXPIRED offer staged nothing — only a rejection is remembered as a refusal")
	}
	if n := countIn(t, e, `SELECT count(*) FROM project_link_candidate WHERE activity_id = $1 AND status = 'pending'`, replyID); n != 1 {
		t.Fatalf("%d pending candidates for the reply, want 1", n)
	}
}
