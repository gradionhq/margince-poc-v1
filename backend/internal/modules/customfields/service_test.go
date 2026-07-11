// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ctxAs binds a workspace-scoped human principal with the given
// custom_field grant so the pre-database refusal paths can be proven
// without a pool: every refusal under test fires before any SQL runs.
func ctxAs(g principal.ObjectGrant) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"custom_field": g},
			RowScope: principal.RowScopeAll,
		},
	})
}

func fullGrant() principal.ObjectGrant {
	return principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true}
}

// validSpec is a spec that passes Validate, so a test can prove the ONE
// refusal it stacks on top.
func validSpec() FieldSpec {
	return FieldSpec{Object: "deal", Label: "Renewal date", Type: TypeDate, Source: "ui"}
}

func TestCreate_RequiresCreateGrant(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.Create(ctxAs(principal.ObjectGrant{Read: true}), validSpec())
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("create without custom_field.create must be permission-denied, got %v", err)
	}
}

func TestCreate_ValidationRefusalListsEveryViolation(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.Create(ctxAs(fullGrant()), FieldSpec{Object: "widget", Label: " ", Type: "money"})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("invalid spec must return *ValidationError, got %v", err)
	}
	if len(verr.Errors) < 3 {
		t.Fatalf("expected object+label+type violations at least, got %v", verr.Errors)
	}
}

func TestCreate_StructuralLabelRefused(t *testing.T) {
	svc := NewService(nil, nil)
	spec := validSpec()
	spec.Label = "Relationship to partner org"
	if _, err := svc.Create(ctxAs(fullGrant()), spec); !errors.Is(err, ErrStructural) {
		t.Fatalf("structural label must be refused with ErrStructural, got %v", err)
	}
}

func TestCreate_UnwiredSchemaPoolRefusesSchemaChange(t *testing.T) {
	svc := NewService(nil, nil)
	if _, err := svc.Create(ctxAs(fullGrant()), validSpec()); !errors.Is(err, ErrSchemaChangesUnavailable) {
		t.Fatalf("nil schema pool must refuse with ErrSchemaChangesUnavailable, got %v", err)
	}
}

func TestRename_RequiresUpdateGrant(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.Rename(ctxAs(principal.ObjectGrant{Read: true}), ids.NewV7(), "New label", nil)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("rename without custom_field.update must be permission-denied, got %v", err)
	}
}

func TestRename_EmptyLabelRefused(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.Rename(ctxAs(fullGrant()), ids.NewV7(), "   ", nil)
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("blank label must return *ValidationError, got %v", err)
	}
}

func TestRetire_RequiresUpdateGrant(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.Retire(ctxAs(principal.ObjectGrant{Read: true}), ids.NewV7())
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("retire without custom_field.update must be permission-denied, got %v", err)
	}
}

func TestSetOptions_EmptySetRefused(t *testing.T) {
	svc := NewService(nil, nil)
	if _, err := svc.SetOptions(ctxAs(fullGrant()), ids.NewV7(), nil); !errors.Is(err, ErrLastOption) {
		t.Fatalf("removing every option must be refused with ErrLastOption, got %v", err)
	}
}

func TestSetOptions_MalformedOptionTextRefused(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.SetOptions(ctxAs(fullGrant()), ids.NewV7(), []string{"ok", "bad\x00opt"})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("a NUL-carrying option must return *ValidationError, got %v", err)
	}
}

func TestSetOptions_UnwiredSchemaPoolRefusesSchemaChange(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.SetOptions(ctxAs(fullGrant()), ids.NewV7(), []string{"a", "b"})
	if !errors.Is(err, ErrSchemaChangesUnavailable) {
		t.Fatalf("nil schema pool must refuse with ErrSchemaChangesUnavailable, got %v", err)
	}
}

func TestList_RequiresReadGrant(t *testing.T) {
	svc := NewService(nil, nil)
	_, _, err := svc.List(ctxAs(principal.ObjectGrant{Create: true}), ListInput{Object: "deal"})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("list without custom_field.read must be permission-denied, got %v", err)
	}
}

func TestList_UnsupportedObjectRefused(t *testing.T) {
	svc := NewService(nil, nil)
	_, _, err := svc.List(ctxAs(fullGrant()), ListInput{Object: "widget"})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("unknown object must return *ValidationError, got %v", err)
	}
}

func TestColumnTakenErrorReadsAsConflict(t *testing.T) {
	err := &ColumnTakenError{Column: "cf_renewal_date"}
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatal("ColumnTakenError must map onto the 409 conflict sentinel")
	}
}
