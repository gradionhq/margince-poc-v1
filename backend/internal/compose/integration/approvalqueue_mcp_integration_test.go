// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The 🟡 loop closed from ONE conversation, against real Postgres.
//
// Until the queue tools existed the loop had a hole where its other half should
// be: a refused call staged a proposal, told the agent to wait for a person, and
// then neither of them could reach the queue without opening the web app. The
// agent could not even see that its own proposal was already waiting.
//
// What is proven here is the whole round trip on one credential — stage, see,
// read, answer, redeem — plus the two bounds that make it safe to admit at all:
// the answer is recorded as the PERSON's, not the credential's, and a passport
// its human lent for reading cannot answer anything.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// queueEnv is one app plus the governed registry the api role composes.
type queueEnv struct {
	*apptest.AppEnv
	registry *agents.Registry
	auth     *identity.Service
}

func setupQueue(t *testing.T) *queueEnv {
	t.Helper()
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	return &queueEnv{AppEnv: e, registry: compose.NewRegistry(e.Pool, compose.SendPath{}), auth: identity.NewService(e.Pool)}
}

// invoker re-authenticates the passport per call, exactly as every transport
// does: a credential revoked between two calls stops at the second one.
func (q *queueEnv) invoker(t *testing.T, token string) func(tool, args string) (string, error) {
	t.Helper()
	return func(tool, args string) (string, error) {
		ws, err := q.auth.InstallationWorkspace(context.Background())
		if err != nil {
			t.Fatalf("resolving the installation workspace: %v", err)
		}
		ctx := principal.WithWorkspaceID(context.Background(), ws.UUID)
		agent, err := q.auth.AuthenticateAgent(ctx, token)
		if err != nil {
			t.Fatalf("authenticating the passport: %v", err)
		}
		ctx = principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7())
		out, invokeErr := q.registry.Invoke(ctx, tool, json.RawMessage(args))
		return string(out), invokeErr
	}
}

func (q *queueEnv) mintPassport(t *testing.T, label string, scopes ...string) string {
	t.Helper()
	var minted struct {
		Token string `json:"token"`
	}
	if status := q.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": label, "scopes": scopes,
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issuing passport %q → %d", label, status)
	}
	return minted.Token
}

// stageAnArchive refuses one 🟡 call and answers the proposal it left behind.
func (q *queueEnv) stageAnArchive(t *testing.T, invoke func(tool, args string) (string, error), name string) (personID string, approvalID ids.UUID) {
	t.Helper()
	var person struct {
		ID string `json:"id"`
	}
	if status := q.Call(t, "POST", "/v1/people", apptest.AnyMap{"full_name": name}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	_, err := invoke("archive_record", `{"record_type":"person","id":"`+person.ID+`"}`)
	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatalf("archive_record → %v, want a staged approval", err)
	}
	return person.ID, staged.ApprovalID.UUID
}

type queueItem struct {
	StagedActionID string          `json:"staged_action_id"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	Summary        string          `json:"summary"`
	TargetType     string          `json:"target_type"`
	TargetID       string          `json:"target_id"`
	DecidedBy      *string         `json:"decided_by"`
	BundleID       *string         `json:"bundle_id"`
	ProposedChange json.RawMessage `json:"proposed_change"`
}

// answered unwraps the surface's result envelope, which every tool answer
// carries: the payload a caller reads is under `data`.
func answered[T any](t *testing.T, out string) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decoding a tool answer: %v", err)
	}
	return envelope.Data
}

// The whole round trip, on the credentials it really takes: the one that
// proposed the action sees it and reads it, a SECOND one answers it, and the
// first redeems what was released. The split is the rule, not the harness — a
// credential does not confirm its own proposal.
func TestAStagedCallIsSeenAndAnsweredFromTheConversationThatStagedIt(t *testing.T) {
	q := setupQueue(t)
	invoke := q.invoker(t, q.mintPassport(t, "proposing agent", "read", "write"))
	personID, approvalID := q.stageAnArchive(t, invoke, "Queue Subject")

	// SEE IT. The proposal the agent could not perform is in the queue it can
	// read, named the way it was staged.
	out, err := invoke("list_approvals", `{}`)
	if err != nil {
		t.Fatalf("list_approvals → %v", err)
	}
	listing := answered[struct {
		Approvals []queueItem `json:"approvals"`
	}](t, out)
	var found bool
	for _, item := range listing.Approvals {
		if item.StagedActionID != approvalID.String() {
			continue
		}
		found = true
		if item.Kind != "archive_record" || item.Status != "pending" {
			t.Errorf("the staged item reads %s/%s, want archive_record/pending", item.Kind, item.Status)
		}
		if item.TargetType != "person" || item.TargetID != personID {
			t.Errorf("the item points at %s/%s, want person/%s", item.TargetType, item.TargetID, personID)
		}
		if item.Summary == "" {
			t.Error("the item carries no sentence a person could answer from")
		}
		// The listing is scanned to choose; the staged document is what
		// read_approval is for. A queue that carried every payload would spend a
		// run's window on documents nobody asked to see.
		if len(item.ProposedChange) != 0 {
			t.Errorf("the listing carries the staged change (%s); that is read_approval's answer", item.ProposedChange)
		}
		// A member with nothing to say is ABSENT, never a zero uuid: this
		// proposal is waiting, and a decided_by printed as 00000000-0000-…
		// tells a caller that somebody has already answered it.
		if item.DecidedBy != nil {
			t.Errorf("a pending item names %v as having decided it", *item.DecidedBy)
		}
		if item.BundleID != nil {
			t.Errorf("an item staged alone claims to belong to act %v", *item.BundleID)
		}
	}
	if !found {
		t.Fatalf("the proposal this agent staged (%s) is not in the queue it can read", approvalID)
	}

	// READ IT. What approving would actually do.
	readArgs := `{"staged_action_id":"` + approvalID.String() + `"}`
	if out, err = invoke("read_approval", readArgs); err != nil {
		t.Fatalf("read_approval %s → %v", readArgs, err)
	}
	one := answered[queueItem](t, out)
	if len(one.ProposedChange) == 0 {
		t.Error("read_approval answered without the change it proposes — there is nothing to decide from")
	}

	// THE PROPOSER DOES NOT ANSWER IT. Approving the row it staged would be the
	// confirm-first act performed on itself.
	answer := `{"staged_action_id":"` + approvalID.String() + `","decision":"approve"}`
	if _, err = invoke("decide_approval", answer); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("the proposer approving its own proposal → %v, want ErrPermissionDenied", err)
	}

	// ANSWER IT on another of the same human's credentials, and the answer is
	// the PERSON's: decided_by is the human who lent it, never the credential.
	decider := q.invoker(t, q.mintPassport(t, "deciding agent", "read", "write"))
	if _, err = decider("decide_approval", answer); err != nil {
		t.Fatalf("decide_approval → %v", err)
	}
	var status string
	var decidedIsTheLender bool
	if err := q.Owner.QueryRow(t.Context(), `
		SELECT a.status, a.decided_by = p.on_behalf_of
		  FROM approval a JOIN passport p ON p.id = a.passport_id
		 WHERE a.id = $1`, approvalID).Scan(&status, &decidedIsTheLender); err != nil {
		t.Fatalf("reading the decided approval: %v", err)
	}
	if status != "approved" {
		t.Fatalf("the approval reads %q after its decision", status)
	}
	if !decidedIsTheLender {
		t.Error("the decision was recorded against somebody other than the person who lent the passport")
	}

	// REDEEM IT, on the credential that staged it: approving does not perform an
	// agent's staged call, the agent re-issues it, and the approval fits only
	// the caller it was staged by.
	if _, err = invoke("archive_record",
		`{"record_type":"person","id":"`+personID+`","approval_id":"`+approvalID.String()+`"}`); err != nil {
		t.Fatalf("the released retry → %v", err)
	}
	var archived bool
	if err := q.Owner.QueryRow(t.Context(),
		`SELECT archived_at IS NOT NULL FROM person WHERE id = $1`, personID).Scan(&archived); err != nil {
		t.Fatalf("reading the person back: %v", err)
	}
	if !archived {
		t.Error("the released call did not perform — the loop closes on paper only")
	}
}

// A credential lent for reading answers nothing. Acting as somebody is not the
// same as holding everything they hold: the caps are the half of a passport its
// human chose, and a decision spends write.
func TestAReadOnlyPassportSeesTheQueueAndCannotAnswerIt(t *testing.T) {
	q := setupQueue(t)
	_, approvalID := q.stageAnArchive(t, q.invoker(t, q.mintPassport(t, "staging agent", "read", "write")), "Read Only Subject")

	reader := q.invoker(t, q.mintPassport(t, "reading agent", "read"))
	if _, err := reader("list_approvals", `{}`); err != nil {
		t.Fatalf("a read passport cannot see the queue: %v", err)
	}
	// Refused at the admission gate, on the cap alone: the door does not need to
	// know which proposal this is to know this credential answers none.
	_, err := reader("decide_approval", `{"staged_action_id":"`+approvalID.String()+`","decision":"approve"}`)
	if !errors.Is(err, apperrors.ErrScopeExceeded) {
		t.Fatalf("a read passport deciding → %v, want the scope refusal", err)
	}
	var status string
	if err := q.Owner.QueryRow(t.Context(), `SELECT status FROM approval WHERE id = $1`, approvalID).Scan(&status); err != nil {
		t.Fatalf("reading the approval back: %v", err)
	}
	if status != "pending" {
		t.Errorf("the proposal reads %q after a refused decision — something was written anyway", status)
	}
}
