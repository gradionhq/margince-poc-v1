// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"errors"
	"testing"

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
