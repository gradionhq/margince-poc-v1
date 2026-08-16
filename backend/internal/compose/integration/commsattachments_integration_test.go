// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The send-time attachment authority, against a real database.
//
// A delivery is not sent when it is staged. It sits on a retry ladder, and in
// that window a file can be archived and a sender can lose the grant that let
// them attach it. EnsureTransmittable is what re-asks — and it is the ONLY
// thing standing between a withdrawn grant and a file leaving the building
// under the sender's own address.
//
// It had no test at all. The existing send suite wires this adapter and then
// says so in its own comment: "this lane sends no files". So the production
// object was constructed and never asked a question.
//
// A162/ADR-0111 sharpened why that matters. The virus scan used to sit beside
// this check; with it retired, the sender's live row scope is the whole gate.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// sendWorkerCtx is the context a send job actually runs under: a workspace and
// a correlation id, and no session — the authority is rebuilt per delivery from
// the sender's live grants, which is the behaviour under test.
func sendWorkerCtx(ws ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// grantOwnScopeRepRole gives a user a real role row granting person:read at
// own row scope, and assigns it.
//
// It has to be a real row. This authority deliberately ignores whatever
// permissions a caller supplies on the context — it RE-READS the sender's
// grants from the database, which is the entire reason it runs at transmit
// time rather than trusting what staging recorded. The harness seeds users and
// teams but no roles at all, so without this every sender resolves to empty
// object grants and every case refuses, including the one that must not.
func grantOwnScopeRepRole(t *testing.T, e *Env, user ids.UUID) {
	t.Helper()
	roleKey := "sendrep-" + user.String()[:8]
	e.WsExec(t, `INSERT INTO role (workspace_id, key, name, permissions)
		VALUES ($1, $2, 'Send Rep', $3::jsonb)`,
		e.WS, roleKey,
		`{"objects":{"person":{"read":true}},"row_scope":"own"}`)
	e.WsExec(t, `INSERT INTO role_assignment (workspace_id, role_id, user_id)
		SELECT $1, r.id, $2 FROM role r WHERE r.workspace_id = $1 AND r.key = $3`,
		e.WS, user, roleKey)
}

// A file its sender can still read transmits. Without this case the refusals
// below would pass against an authority that refuses everything.
func TestEnsureTransmittableAdmitsAFileTheSenderCanStillRead(t *testing.T) {
	e := Setup(t)
	store, blob := attachmentStore(e)
	grantOwnScopeRepRole(t, e, e.Rep1)
	person := e.SeedPerson(t, "Rep1's Person", &e.Rep1)

	att, err := store.UploadAttachment(e.Admin(), activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "offer.pdf", Body: []byte("PDF"),
	})
	if err != nil {
		t.Fatalf("seeding the attachment through the real writer: %v", err)
	}

	authority := compose.NewSendAttachmentAuthority(e.Pool, blob)
	ok, reason, err := authority.EnsureTransmittable(
		sendWorkerCtx(e.WS), ids.From[ids.UserKind](e.Rep1), []ids.UUID{ids.UUID(att.Id)})

	if err != nil {
		t.Fatalf("EnsureTransmittable: %v", err)
	}
	if !ok {
		t.Errorf("a file its owner can read was refused: %q", reason)
	}
	if reason != "" {
		t.Errorf("reason = %q on an admitted send, want empty", reason)
	}
}

// The sender lost the grant between staging and transmit. The file is
// untouched; the authority is not — and the message must park rather than
// carry a document its sender may no longer read.
func TestEnsureTransmittableRefusesAFileTheSenderCanNoLongerSee(t *testing.T) {
	e := Setup(t)
	store, blob := attachmentStore(e)
	// Rep3 holds the SAME role as the sender in the admitting case above, so
	// the only thing separating them is row scope — which is what this proves.
	grantOwnScopeRepRole(t, e, e.Rep3)
	// Owned by Rep1; Rep3 sits in the other team and never had access.
	person := e.SeedPerson(t, "Rep1's Person", &e.Rep1)

	att, err := store.UploadAttachment(e.Admin(), activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "private.pdf", Body: []byte("PDF"),
	})
	if err != nil {
		t.Fatalf("seeding the attachment: %v", err)
	}

	authority := compose.NewSendAttachmentAuthority(e.Pool, blob)
	ok, reason, err := authority.EnsureTransmittable(
		sendWorkerCtx(e.WS), ids.From[ids.UserKind](e.Rep3), []ids.UUID{ids.UUID(att.Id)})

	if err != nil {
		t.Fatalf("EnsureTransmittable: %v", err)
	}
	if ok {
		t.Fatal("a file outside the sender's row scope was cleared for transmit")
	}
	// The reason is what an operator reads off the parked delivery, so it has
	// to say what to do. It deliberately does NOT distinguish "archived" from
	// "you lost access": telling them apart would confirm to someone whose
	// access was withdrawn that the document still exists.
	if reason == "" {
		t.Error("the refusal carries no reason, so a parked delivery explains nothing")
	}
}

// An attachment id that never existed is refused exactly like one the sender
// cannot see. A send path that answered differently would let a caller probe
// which ids are real by watching how the delivery parks.
func TestEnsureTransmittableTreatsAnUnknownFileLikeAnInvisibleOne(t *testing.T) {
	e := Setup(t)
	_, blob := attachmentStore(e)
	grantOwnScopeRepRole(t, e, e.Rep1)

	authority := compose.NewSendAttachmentAuthority(e.Pool, blob)
	ok, reason, err := authority.EnsureTransmittable(
		sendWorkerCtx(e.WS), ids.From[ids.UserKind](e.Rep1), []ids.UUID{ids.NewV7()})

	if err != nil {
		t.Fatalf("EnsureTransmittable on an unknown id: %v", err)
	}
	if ok {
		t.Fatal("an attachment id that does not exist was cleared for transmit")
	}
	if reason == "" {
		t.Error("the refusal carries no reason")
	}
}

// Outside a workspace the authority FAULTS rather than answering no.
//
// The distinction is load-bearing: the dispatcher parks a delivery on a (false,
// reason) answer and retries it on an error. A missing workspace is a wiring
// defect, and parking every message in the batch over it would destroy
// legitimate sends that a fixed deployment would have delivered.
func TestEnsureTransmittableFaultsRatherThanRefusingWithoutAWorkspace(t *testing.T) {
	e := Setup(t)
	_, blob := attachmentStore(e)

	authority := compose.NewSendAttachmentAuthority(e.Pool, blob)
	_, _, err := authority.EnsureTransmittable(
		context.Background(), ids.From[ids.UserKind](e.Rep1), []ids.UUID{ids.NewV7()})

	if err == nil {
		t.Fatal("a send outside workspace context answered instead of faulting; the dispatcher would park rather than retry")
	}
}
