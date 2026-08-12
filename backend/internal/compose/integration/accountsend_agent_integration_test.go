// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The account-started send under agent authority, over the real database:
// ADR-0087 §6's whole claim, which is that this operation is governed exactly
// like the reply and gains nobody any new authority.
//
// Three facts, and none of them is observable without a database:
//
//   - a human's own send goes straight out (ADR-0055 — their action IS the
//     confirmation), which is what makes the agent's refusal below a statement
//     about the PRINCIPAL rather than about a send path that does not work;
//   - an agent's identical call stages a real approval row instead, and the
//     row it stages is the CREATE shape — the type the effect will write, no
//     target id, because this send answers no message and pins no row;
//   - a human can then see it, release it, and only then does the message
//     leave — with the outbound activity filed under the records the call
//     named, which is the one thing the account-started origin does
//     differently from the reply.
//
// It rides send_preflight's fixture: a consented recipient on file and a
// mailbox that can actually transmit, so an acceptance here is a real send and
// not a refusal in disguise.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// accountSendBody is the one call this suite makes, so the human's send and the
// agent's differ in the CREDENTIAL and in nothing else. The approved retry has
// to be the identical request — the diff hash binds it — which is a second
// reason it is built in one place.
func accountSendBody(org, subject string) apptest.AnyMap {
	return apptest.AnyMap{
		"subject": subject, "body": "Good morning — introducing ourselves.",
		"to": []string{"buyer@preflight.test"}, "consent_purpose": "transactional",
		"links": []apptest.AnyMap{{"entity_type": "organization", "entity_id": org}},
	}
}

// accountSendEnv is the pre-flight fixture plus the record an account-started
// conversation is filed under.
type accountSendEnv struct {
	*preflightEnv
	org string
}

func setupAccountSend(t *testing.T) *accountSendEnv {
	t.Helper()
	p := setupPreflight(t)
	// Without the send grant every send below refuses at the pre-flight, and
	// this suite would pass while proving nothing about authority.
	p.connect(t, gmailReadonlyScope, gmailSendScope)
	return &accountSendEnv{preflightEnv: p, org: anchorOrg(t, p.AppEnv, "Northwind")}
}

// deliveryCount is what a send did or did not do, read from the table both
// transports write: a refusal that leaves it empty refused on the tool surface
// too.
func (a *accountSendEnv) deliveryCount(t *testing.T) int {
	t.Helper()
	return a.stagedDeliveries(t)
}

// linkedActivities counts the outbound activities filed under the organization
// this suite names — the account-started origin's own effect, since a reply
// would have inherited its links from an anchor instead.
func (a *accountSendEnv) linkedActivities(t *testing.T) int {
	t.Helper()
	var n int
	if err := apptest.InWorkspace(a.AppEnv, t, a.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT count(*) FROM activity a
			JOIN activity_link l ON l.activity_id = a.id
			WHERE l.organization_id = $1 AND a.direction = 'outbound'`, a.org).Scan(&n)
	}); err != nil {
		t.Fatalf("counting the conversation's activities: %v", err)
	}
	return n
}

func TestAHumansAccountStartedSendLeavesWithoutAnApproval(t *testing.T) {
	a := setupAccountSend(t)

	var sent struct {
		ID string `json:"id"`
	}
	if status := a.Call(t, "POST", "/v1/emails", accountSendBody(a.org, "Hello from Fable"), nil, &sent); status != http.StatusAccepted {
		t.Fatalf("human account-started send → %d, want 202 — a human's own action is the approval", status)
	}
	if sent.ID == "" {
		t.Fatal("the accepted send named no activity")
	}
	if n := a.deliveryCount(t); n != 1 {
		t.Fatalf("%d deliveries staged behind an accepted send, want 1", n)
	}
	if n := a.linkedActivities(t); n != 1 {
		t.Fatalf("%d outbound activities filed under the named organization, want 1", n)
	}
	if n := a.pendingApprovals(t); n != 0 {
		t.Fatalf("%d approvals minted for a human's own send, want none", n)
	}
}

// pendingApprovals counts what is waiting on a human. Zero is the assertion for
// a human's own send; one is the assertion for an agent's.
func (a *accountSendEnv) pendingApprovals(t *testing.T) int {
	t.Helper()
	var n int
	if err := apptest.InWorkspace(a.AppEnv, t, a.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM approval WHERE kind = 'send_account_email'`).Scan(&n)
	}); err != nil {
		t.Fatalf("counting staged approvals: %v", err)
	}
	return n
}

func TestAnAgentsAccountStartedSendStagesTheCreateAndOnlyLeavesOnceApproved(t *testing.T) {
	a := setupAccountSend(t)
	var minted struct {
		Token string `json:"token"`
	}
	if status := a.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": "outreach agent", "scopes": []string{"read", "send"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}
	body := accountSendBody(a.org, "Hello from an agent")

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := a.Call(t, "POST", "/v1/emails", body, bearer, &problem); status != http.StatusForbidden ||
		problem.Code != "approval_required" {
		t.Fatalf("agent account-started send → %d %q, want 403 approval_required", status, problem.Code)
	}
	if n := a.deliveryCount(t); n != 0 {
		t.Fatalf("%d deliveries staged behind an unapproved agent send, want 0 — nothing may leave", n)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// The staged SHAPE, which is what makes the row decidable at all: the type
	// the effect will write, and no row for a scope probe to resolve, because
	// this send answers no message.
	var targetType, targetID *string
	if err := a.Owner.QueryRow(t.Context(),
		`SELECT target_entity_type, target_entity_id FROM approval WHERE id = $1`,
		approvalID).Scan(&targetType, &targetID); err != nil {
		t.Fatal(err)
	}
	if targetType == nil || *targetType != "activity" || targetID != nil {
		t.Fatalf("staged target = %v/%v, want activity with no id", targetType, targetID)
	}

	// A human sees it and releases it. Both halves matter: an approval the
	// inbox cannot show is one nobody can act on, whatever the decision
	// endpoint would have accepted.
	if !a.inboxShows(t, approvalID) {
		t.Fatal("the staged send is not in the approvals inbox; nobody could ever release it")
	}
	if status := a.Call(t, "POST", "/v1/approvals/"+approvalID+"/approve", apptest.AnyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("human approve → %d", status)
	}
	if n := a.deliveryCount(t); n != 0 {
		t.Fatalf("%d deliveries staged by the approval itself, want 0 — approving authorizes, it does not send", n)
	}

	// The identical request, now carrying the released approval. Identical is
	// the requirement, not the style: the diff hash binds the approval to this
	// exact message.
	redeem := map[string]string{"Authorization": "Bearer " + minted.Token, "X-Approval-Token": approvalID}
	if status := a.Call(t, "POST", "/v1/emails", body, redeem, nil); status != http.StatusAccepted {
		t.Fatalf("approved retry → %d, want 202", status)
	}
	if n := a.deliveryCount(t); n != 1 {
		t.Fatalf("%d deliveries after the approved retry, want exactly 1", n)
	}
	if n := a.linkedActivities(t); n != 1 {
		t.Fatalf("%d outbound activities filed under the named organization, want 1 — the links are what "+
			"an account-started send has instead of an anchor", n)
	}

	// Single-use: the same approval cannot send a second message.
	if status := a.Call(t, "POST", "/v1/emails", body, redeem, nil); status == http.StatusAccepted {
		t.Fatal("a consumed approval sent a second message; redemption must be single-use")
	}
	if n := a.deliveryCount(t); n != 1 {
		t.Fatalf("%d deliveries after replaying a consumed approval, want still 1", n)
	}
}

// inboxShows reports whether the acting human's approvals inbox lists the row —
// the read path targetVisible governs, asked over HTTP rather than in SQL so
// the answer is the one a person would actually get.
func (a *accountSendEnv) inboxShows(t *testing.T, approvalID string) bool {
	t.Helper()
	var page struct {
		Data []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"data"`
	}
	if status := a.Call(t, "GET", "/v1/approvals?status=pending", nil, nil, &page); status != http.StatusOK {
		t.Fatalf("list approvals → %d", status)
	}
	for _, row := range page.Data {
		if row.ID == approvalID {
			if row.Kind != "send_account_email" {
				t.Errorf("inbox row kind = %q, want send_account_email", row.Kind)
			}
			return true
		}
	}
	return false
}

// The MCP door stages through the tool's own StageInfo rather than through the
// REST gate's route walk, so the two could disagree about what an
// account-started send targets — and a human would then be deciding a
// different question depending on which transport the agent used.
//
// They agree here because the REST gate takes its target from the route and
// this route has no {id}, so the id-less create is the only shape both doors
// can reach. That is a bound on the approver too — read+create on `activity`
// rather than the row scope of the records the message is filed under — and
// #928 is where the gate gains the body-derived target that would lift it.
//
// It drives Registry.Invoke directly, for the reason channelsend_mcp does: the
// JSON-RPC framing is proven elsewhere, and Invoke is the one call every
// transport dispatches through.
func TestTheMCPDoorStagesTheSameShapeAsTheRESTDoor(t *testing.T) {
	a := setupAccountSend(t)
	token := a.mintAccountSendPassport(t)

	args, err := json.Marshal(map[string]any{
		"to": []string{"buyer@preflight.test"}, "subject": "Hello over MCP",
		"body": "Good morning.", "consent_purpose": "transactional",
		"links": []map[string]string{{"entity_type": "organization", "entity_id": a.org}},
	})
	if err != nil {
		t.Fatal(err)
	}

	staged := a.invokeAccountSend(t, token, args)

	var kind string
	var targetType, targetID *string
	if err := a.Owner.QueryRow(t.Context(),
		`SELECT kind, target_entity_type, target_entity_id FROM approval WHERE id = $1`,
		staged.String()).Scan(&kind, &targetType, &targetID); err != nil {
		t.Fatal(err)
	}
	if kind != "send_account_email" {
		t.Errorf("staged kind = %q, want send_account_email — the decision grants are looked up by it", kind)
	}
	if targetType == nil || *targetType != "activity" || targetID != nil {
		t.Fatalf("the MCP door staged %v/%v, want the REST door's activity with no id", targetType, targetID)
	}
}

// mintAccountSendPassport issues the credential an outreach agent presents.
func (a *accountSendEnv) mintAccountSendPassport(t *testing.T) string {
	t.Helper()
	var minted struct {
		Token string `json:"token"`
	}
	if status := a.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": "outreach agent", "scopes": []string{"read", "send"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	return minted.Token
}

// invokeAccountSend calls the tool through the governed registry the api role
// composes, re-authenticating the passport exactly as every transport does, and
// returns the approval its refusal staged.
//
// The SendPath is empty because nothing here is ever redeemed: this test is
// about what the refusal STAGES, and the send itself is covered end to end by
// the REST loop above. A delivery machinery wired here would be scenery.
func (a *accountSendEnv) invokeAccountSend(t *testing.T, token string, args json.RawMessage) ids.ApprovalID {
	t.Helper()
	registry := compose.NewRegistry(a.Pool, compose.SendPath{})
	authSvc := identity.NewService(a.Pool)
	wsID, err := authSvc.InstallationWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx := principal.WithWorkspaceID(t.Context(), wsID.UUID)
	agent, err := authSvc.AuthenticateAgent(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx = principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7())

	_, invokeErr := registry.Invoke(ctx, "send_account_email", args)

	var staged *workflow.StagedApprovalError
	if !errors.As(invokeErr, &staged) {
		t.Fatalf("tools/call send_account_email → %v, want it to stage an approval", invokeErr)
	}
	if staged.ApprovalID.IsZero() {
		t.Fatal("the staged refusal carries a zero approval id")
	}
	return staged.ApprovalID
}
