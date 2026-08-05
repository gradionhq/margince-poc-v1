// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// writableEntityTypes are the five types the mirror carries. Spelled here
// rather than derived from a production list on purpose: a test that reads
// the same list the code under test reads cannot notice the list changing.
var writableEntityTypes = []datasource.EntityType{
	datasource.EntityPerson,
	datasource.EntityOrganization,
	datasource.EntityDeal,
	datasource.EntityLead,
	datasource.EntityActivity,
}

// fullyGrantedActor is a principal holding every object grant on every
// mirrored type, with unrestricted row scope — so a refusal in the tests
// below can only come from the capability gate, never from authorization.
func fullyGrantedActor() context.Context {
	objects := make(map[string]principal.ObjectGrant, len(writableEntityTypes))
	for _, et := range writableEntityTypes {
		objects[string(et)] = principal.ObjectGrant{Create: true, Update: true, Delete: true, Read: true}
	}
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:fully-granted",
		Permissions: principal.Permissions{Objects: objects, RowScope: principal.RowScopeAll},
	})
}

// TestWriteVerbsRefuseExactlyWhatSupportsWriteDenies derives the obligation
// from the declaration instead of restating it: for EVERY (verb, entity)
// pair, the provider's own write method must refuse precisely when
// SupportsWrite says it cannot serve that pair.
//
// This is the invariant the composition layer's REST guard and the write
// shadows both READ. Before this gate existed the declaration was enforced
// only by that guard, so the agent tool surface and the automation engine —
// which reach these verbs through the datasource seam with no router in
// between — could execute a verb the mirror declares impossible. A capability
// only one transport honors is not a capability.
//
// A refused pair must never touch the incumbent: the Provider here has no
// incumbent resolver wired, so any verb that got past the gate would fail
// with the resolver error instead — a distinct, recognizable failure.
func TestWriteVerbsRefuseExactlyWhatSupportsWriteDenies(t *testing.T) {
	p := NewProvider(&MirrorStore{}, nil)
	ctx := fullyGrantedActor()

	for _, et := range writableEntityTypes {
		for _, verb := range []WriteVerb{WriteCreate, WriteUpdate, WriteArchive} {
			ref := datasource.EntityRef{Type: et, ID: ids.NewV7()}
			var err error
			switch verb {
			case WriteCreate:
				_, err = p.Create(ctx, datasource.CreateInput{EntityType: et})
			case WriteUpdate:
				_, err = p.Update(ctx, datasource.UpdateInput{Ref: ref})
			case WriteArchive:
				_, err = p.Archive(ctx, ref)
			}

			refused := errors.Is(err, apperrors.ErrUnsupportedBySoR)
			if want := SupportsWrite(verb, et); want == refused {
				t.Errorf("%s %s: SupportsWrite=%v but the verb %s (err = %v)",
					verb, et, want, refusalWord(refused), err)
			}
			if refused {
				continue
			}
			// A pair the provider DOES serve must have reached the incumbent
			// resolver — proof the gate let it through rather than refusing it
			// under some other error.
			if err == nil || errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Errorf("%s %s is declared supported but did not reach the incumbent: err = %v", verb, et, err)
			}
		}
	}
}

// refusalWord renders the observed outcome for a failure message.
func refusalWord(refused bool) string {
	if refused {
		return "refused it as unsupported"
	}
	return "did not refuse it"
}

// TestAuditImagesNeverCarryContentFreeFieldValues is the data-minimization
// gate on the write-back audit trail: a field declared content-free must
// reach audit_log as a presence flag, never as its value.
//
// It matters more here than on the native path. A mirrored activity body is
// INCUMBENT-sourced customer correspondence, and audit_log is append-only,
// retained under the compliance floor, and served verbatim to unbounded
// readers — so a value copied there survives both Art. 17 erasure and
// disconnect teardown, neither of which reaches audit_log.
//
// The obligation is derived from contentFreeAuditFields rather than restated,
// so declaring a new content-free field gates it automatically.
func TestAuditImagesNeverCarryContentFreeFieldValues(t *testing.T) {
	for et, contentFree := range contentFreeAuditFields {
		for field := range contentFree {
			image := map[string]any{
				field:   "the incumbent's own message text",
				"other": "kept as-is",
			}
			got := minimizeAuditImage(et, image)

			if got[field] != true {
				t.Errorf("%s %s in an audit image = %v, want the presence flag true — the value must never reach audit_log",
					et, field, got[field])
			}
			if got["other"] != "kept as-is" {
				t.Errorf("%s: minimizing dropped an unrelated field (%v); only content-free fields are narrowed", et, got["other"])
			}
			if image[field] != "the incumbent's own message text" {
				t.Errorf("%s: minimizing mutated the caller's map — the patch is still needed downstream for the incumbent write", et)
			}
		}
	}
}

// TestAfterIncumbentCommitOutlivesACancelledCaller pins the property the
// local half of a write-back depends on: once the incumbent has committed,
// the caller going away must not abandon the bookkeeping.
//
// Without it, a user who fires an archive and closes the tab leaves a record
// that no longer exists at the incumbent still listed, still readable, and
// with no audit row — until the next full reconcile sweep. The context must
// also keep carrying its values, since the workspace binding the RLS GUC and
// the audit attribution both read lives there.
func TestAfterIncumbentCommitOutlivesACancelledCaller(t *testing.T) {
	type ctxKey string
	const bound ctxKey = "workspace"

	parent, cancel := context.WithCancel(context.WithValue(context.Background(), bound, "ws-1"))
	local, release := afterIncumbentCommit(parent)
	defer release()

	cancel() // the caller hangs up

	if err := local.Err(); err != nil {
		t.Errorf("the local half's context died with its caller: %v — the incumbent write it describes has already committed", err)
	}
	if got := local.Value(bound); got != "ws-1" {
		t.Errorf("the local half lost its context values (workspace = %v); the RLS binding and audit attribution both read from there", got)
	}
	if _, hasDeadline := local.Deadline(); !hasDeadline {
		t.Error("the local half has no deadline of its own — detached work without one hangs a request worker on a stalled database")
	}
}

// TestWritePathErrorCollapsesTheFenceSignal proves the disconnect fence's
// internal control signal does not escape the write path as an opaque 500.
// A disconnect racing an in-flight write is "this workspace no longer reads
// from an incumbent" — the same 404 every other post-disconnect request
// answers — not a server fault. Anything else passes through untouched, so
// a real failure is never masked as a routing answer.
func TestWritePathErrorCollapsesTheFenceSignal(t *testing.T) {
	if err := writePathError(ErrConnectionGone); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("writePathError(ErrConnectionGone) = %v, want ErrModeNotOverlay", err)
	}
	if err := writePathError(fmt.Errorf("wrapped: %w", ErrConnectionGone)); !errors.Is(err, apperrors.ErrModeNotOverlay) {
		t.Errorf("writePathError on a wrapped fence signal = %v, want ErrModeNotOverlay", err)
	}
	other := errors.New("the database went away")
	if err := writePathError(other); !errors.Is(err, other) {
		t.Errorf("writePathError(%v) = %v, want it passed through — collapsing it would hide a real failure behind a 404", other, err)
	}
	if writePathError(nil) != nil {
		t.Error("writePathError(nil) must stay nil")
	}
}

// TestWriteLedgerPruneExpiredRequiresWorkspace: the prune runs under
// WithWorkspaceTx, so a context with no workspace bound fails closed with a
// surfaced error rather than a cross-tenant or unscoped delete. The fence is
// checked before the pool is dialled, which a nil pool proves rather than
// assumes.
func TestWriteLedgerPruneExpiredRequiresWorkspace(t *testing.T) {
	l := NewWriteLedger(nil)
	if _, err := l.PruneExpired(context.Background()); !errors.Is(err, database.ErrNoWorkspace) {
		t.Errorf("PruneExpired without a workspace-bound context = %v, want ErrNoWorkspace — anything else risks an unscoped delete", err)
	}
}

// TestArchiveAuditImagesStayNil proves an archive's images survive
// minimization as nil. An archive changes no field values — the native
// archive audits (nil, nil) — and an empty object in their place would read
// as "every field changed to nothing" in a field-history projection.
func TestArchiveAuditImagesStayNil(t *testing.T) {
	if got := minimizeAuditImage(datasource.EntityActivity, nil); got != nil {
		t.Errorf("minimizing a nil audit image = %v, want nil", got)
	}
}

// TestUnsupportedWriteNamesTheEntityWhenTheMirrorDoesNotCarryIt proves the
// two refusals are distinguishable: a verb the mirror cannot serve for a type
// it DOES carry is "unsupported by this system of record", while a type the
// mirror does not carry at all is an unsupported ENTITY — a caller told the
// wrong one would chase the wrong fix.
func TestUnsupportedWriteNamesTheEntityWhenTheMirrorDoesNotCarryIt(t *testing.T) {
	p := NewProvider(&MirrorStore{}, nil)
	ctx := fullyGrantedActor()

	// A carried type, an unservable verb.
	if _, err := p.Archive(ctx, datasource.EntityRef{Type: datasource.EntityLead, ID: ids.NewV7()}); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("archiving a lead: err = %v, want ErrUnsupportedBySoR", err)
	}

	// A type the mirror never carries.
	var unsupported *datasource.UnsupportedEntityError
	_, err := p.Update(ctx, datasource.UpdateInput{
		Ref: datasource.EntityRef{Type: datasource.EntityType("pipeline"), ID: ids.NewV7()},
	})
	if !errors.As(err, &unsupported) {
		t.Errorf("updating a pipeline: err = %v, want UnsupportedEntityError", err)
	}
}
