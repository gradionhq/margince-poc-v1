// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// Which probe each staged target type takes, and why that probe and no other.
// The classification is the whole authority decision for a target: too wide and
// the inbox discloses a record its owning store hides, too narrow and the staged
// row is stranded where no human can release or reject it. These cases are pure
// — they assert the classification and the mechanism that makes the rejected
// alternative wrong; what the chosen probe then answers over real rows is the
// compose integration lane's job.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestEveryTargetProbeMirrorsItsOwningStoresReadRule(t *testing.T) {
	for _, c := range []struct {
		targetType string
		want       targetProbe
		because    string
	}{
		{
			targetType: tableList, want: probeOwnScope,
			because: "collections' list reads ARE auth.EnsureVisible over `list`, owner_id and all",
		},
		{
			targetType: targetTag, want: probeExistence,
			because: "a tag carries no owner_id; its store's reads are object-gated and workspace-wide",
		},
		{
			targetType: "webhook_subscription", want: probeExistence,
			because: "subscription reads apply NO owner predicate — the object grant governs, workspace-wide",
		},
		{
			targetType: "offer_template", want: probeExistence,
			because: "a template is workspace-shared branding config with no row scope at all",
		},
		{
			targetType: targetSavedView, want: probeOwnerOnly,
			because: "the saved-view store reads back on `id AND owner_id`, which own/team/all is wider than",
		},
		{
			targetType: targetFxRate, want: probeActingWorkspace,
			because: "a proposal for a brand-new currency pair has no rate row yet, so the target IS the workspace",
		},
		{
			targetType: targetAIModelRate, want: probeActingWorkspace,
			because: "the model rate sheet is the same shape as the FX sheet: effective-dated, workspace-scoped",
		},
		{
			targetType: "chartreuse", want: probeNoRule,
			because: "a type nobody classified must fail closed rather than borrow a neighbour's rule",
		},
	} {
		t.Run(c.targetType, func(t *testing.T) {
			if got := probeFor(c.targetType); got != c.want {
				t.Errorf("probeFor(%q) = %d, want %d — %s", c.targetType, got, c.want, c.because)
			}
		})
	}
}

// The workspace-shared targets must never reach the row-scope clause, and this
// is why it matters: platform/auth interpolates only the tables that HAVE an
// owner_id, so asking it about one that does not is an ERROR — a 500 to the
// human opening their inbox, not a refusal. The classification is what keeps
// them off that cliff, so the arm they do take has to answer a clean
// not-visible for a row that is absent.
func TestASharedConfigTargetNeverReachesTheRowScopeClause(t *testing.T) {
	ctx := actorWithScope(principal.RowScopeAll)
	for _, targetType := range []string{targetTag, "webhook_subscription", "offer_template"} {
		t.Run(targetType, func(t *testing.T) {
			if got := probeFor(targetType); got == probeOwnScope {
				t.Fatalf("%q is classified as own-scope, which would ask auth.VisibleTo about a table it "+
					"does not row-scope", targetType)
			}
			if _, err := auth.VisibleTo(ctx, nil, targetType, ids.NewV7()); err == nil {
				t.Errorf("auth.VisibleTo(%q) answered without error — this test guards the arm NOT taken, so "+
					"if the table became row-scoped, revisit the classification rather than deleting this case",
					targetType)
			}
			if _, ok := existenceProbes[targetType]; !ok {
				t.Errorf("%q has no existence query, so its probe would fail loudly instead of refusing", targetType)
			}
		})
	}
}

// A saved view IS a table platform/auth row-scopes, so the own-scope arm would
// not error here — it would simply answer WIDER than the API. This is the
// mechanism: an all-scope human's row-scope clause is empty, so auth.VisibleTo
// admits every row in the workspace, including the private views the saved-view
// store serves to their owner alone. Ownership, not scope, is the probe.
func TestAPersonalTargetIsProbedByOwnershipNotRowScope(t *testing.T) {
	if got := probeFor(targetSavedView); got != probeOwnerOnly {
		t.Fatalf("probeFor(%q) = %d, want probeOwnerOnly", targetSavedView, got)
	}
	clause, err := auth.ScopeClauseFor(actorWithScope(principal.RowScopeAll), targetSavedView, "", func(any) int { return 1 })
	if err != nil {
		t.Fatalf("saved_view is expected to BE row-scoped by platform/auth: %v", err)
	}
	if clause != "" {
		t.Errorf("an all-scope human's saved_view clause is %q, want empty — this case exists because an "+
			"empty clause means every row, which is the widening the ownership probe avoids", clause)
	}
}

// The two totals that keep the classification honest: a type routed to a probe
// with no query behind it fails LOUDLY. A wrong answer against some other
// type's table is worse than an error, because it reads as a decision.
func TestAClassifiedTargetWithNoQueryFailsLoudly(t *testing.T) {
	if _, err := targetExists(context.Background(), nil, "chartreuse", ids.NewV7()); err == nil {
		t.Error("targetExists answered for a type with no existence query, instead of naming the gap")
	}
	if _, err := targetOwnedByActor(context.Background(), nil, "chartreuse", ids.NewV7()); err == nil {
		t.Error("targetOwnedByActor answered for a type with no ownership query, instead of naming the gap")
	}
}

// A principal with no human identity owns no personal state. It must read as
// not-visible rather than be compared against owner_id, where the nil UUID
// would ask about rows nobody owns.
func TestAPersonalTargetIsInvisibleToAPrincipalWithNoHumanIdentity(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	visible, err := targetOwnedByActor(ctx, nil, targetSavedView, ids.NewV7())
	if err != nil {
		t.Fatalf("targetOwnedByActor: %v — the nil tx proves no query was issued, so this must refuse before one", err)
	}
	if visible {
		t.Error("a principal with no human identity was shown personal state")
	}
}

// actorWithScope binds a human holding every object grant at one row-scope
// tier. The grants are irrelevant to the clauses under test here and present
// only so RBAC resolution succeeds.
func actorWithScope(scope principal.RowScope) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type:        principal.PrincipalHuman,
		UserID:      ids.NewV7(),
		Permissions: principal.Permissions{RowScope: scope},
	})
}
