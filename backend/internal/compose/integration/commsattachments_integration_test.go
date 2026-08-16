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
	if reason == "" {
		t.Error("the refusal carries no reason, so a parked delivery explains nothing")
	}

	// ROW SCOPE is what refused this, not a missing grant. Rep3 holds
	// person:read — the same object grant the admitted sender holds — so
	// without this the case would pass against a sender who simply holds
	// nothing, and would keep passing if the row-scope clause were deleted.
	// The proof is that the SAME sender, asked about a person they own,
	// is admitted.
	ownPerson := e.SeedPerson(t, "Rep3's Own Person", &e.Rep3)
	ownAtt, err := store.UploadAttachment(e.Admin(), activities.AttachmentInput{
		EntityType: "person", EntityID: ownPerson, Filename: "mine.pdf", Body: []byte("PDF"),
	})
	if err != nil {
		t.Fatalf("seeding the sender's own attachment: %v", err)
	}
	okOwn, ownReason, err := authority.EnsureTransmittable(
		sendWorkerCtx(e.WS), ids.From[ids.UserKind](e.Rep3), []ids.UUID{ids.UUID(ownAtt.Id)})
	if err != nil {
		t.Fatalf("EnsureTransmittable on the sender's own file: %v", err)
	}
	if !okOwn {
		t.Errorf("the same sender was refused a file on a record they OWN (%q) — the refusal above was a missing grant, not row scope", ownReason)
	}
}

// An attachment id that never existed is refused with the BYTE-IDENTICAL
// reason as one the sender cannot see.
//
// This compares the two answers rather than checking each is non-empty,
// because the security claim is indistinguishability and two different
// non-empty sentences would satisfy a per-case check while leaking exactly
// what the check exists to hide: a sender whose access was withdrawn could
// tell "the document still exists" from "no such document" by reading the
// park reason off their own delivery.
func TestEnsureTransmittableRefusesAnUnknownFileIndistinguishablyFromAnInvisibleOne(t *testing.T) {
	e := Setup(t)
	store, blob := attachmentStore(e)
	grantOwnScopeRepRole(t, e, e.Rep3)
	// A real file, owned by Rep1, that Rep3 cannot see.
	person := e.SeedPerson(t, "Rep1's Person", &e.Rep1)
	att, err := store.UploadAttachment(e.Admin(), activities.AttachmentInput{
		EntityType: "person", EntityID: person, Filename: "private.pdf", Body: []byte("PDF"),
	})
	if err != nil {
		t.Fatalf("seeding the attachment: %v", err)
	}

	authority := compose.NewSendAttachmentAuthority(e.Pool, blob)
	sender := ids.From[ids.UserKind](e.Rep3)
	okInvisible, invisible, err := authority.EnsureTransmittable(
		sendWorkerCtx(e.WS), sender, []ids.UUID{ids.UUID(att.Id)})
	if err != nil {
		t.Fatalf("EnsureTransmittable on an invisible file: %v", err)
	}
	okUnknown, unknown, err := authority.EnsureTransmittable(
		sendWorkerCtx(e.WS), sender, []ids.UUID{ids.NewV7()})
	if err != nil {
		t.Fatalf("EnsureTransmittable on an unknown id: %v", err)
	}

	if okInvisible || okUnknown {
		t.Fatalf("cleared for transmit: invisible=%v unknown=%v — both must be refused", okInvisible, okUnknown)
	}
	if invisible != unknown {
		t.Errorf("the two refusals differ, so a sender can tell a withheld file from a missing one:\n invisible = %q\n unknown   = %q", invisible, unknown)
	}
	if invisible == "" {
		t.Error("the refusal carries no reason, so a parked delivery explains nothing")
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
