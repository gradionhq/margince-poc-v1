// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// schemaObjects is hand-written and nothing derives it from the vocabulary it
// describes, so a record type can join the surface — creatable, taggable,
// list-able — while ListFields answers UnsupportedEntityError for it and no
// report can group it. These gates derive the obligation from the vocabulary
// instead.
//
// They run ONE direction deliberately: every RecordType owes a descriptor,
// but not every descriptor is a RecordType. `activity` is the standing
// counter-example — a reportable timeline event that nothing points AT — so
// demanding the reverse would delete a legitimate entry.

// recordTypesWithoutDescriptor names the record types that legitimately have
// no descriptor, each with the reason it cannot be served yet. Empty is the
// correct state: an entry is a promise to come back, not a place to park one.
var recordTypesWithoutDescriptor = gatekit.Waive(map[datasource.RecordType]string{})

func TestEveryRecordTypeHasASchemaDescriptor(t *testing.T) {
	defer recordTypesWithoutDescriptor.AssertAllMatched(t)

	records := datasource.RecordTypes()
	if len(records) == 0 {
		t.Fatal("the record vocabulary is empty — this gate walked nothing, so it proves nothing")
	}
	described := map[datasource.EntityType][]datasource.FieldDef{}
	for _, obj := range schemaObjects {
		described[obj.Type] = obj.Fields
	}
	for _, record := range records {
		if recordTypesWithoutDescriptor.Waived(t, record) {
			continue
		}
		fields, ok := described[datasource.EntityType(record)]
		if !ok {
			t.Errorf("record type %q has no schemaObjects entry: list_fields answers "+
				"UnsupportedEntityError and no report can group it, while create/tag/list all serve it. "+
				"Add the descriptor, or name it in recordTypesWithoutDescriptor with the reason", record)
			continue
		}
		if len(fields) == 0 {
			t.Errorf("record type %q has an empty descriptor — a field list of nothing serves no "+
				"introspection and compiles no report plan", record)
		}
	}
	for _, record := range recordTypesWithoutDescriptor.Subjects() {
		if _, ok := described[datasource.EntityType(record)]; ok {
			t.Errorf("recordTypesWithoutDescriptor[%s] names a type that HAS a descriptor — stale "+
				"waiver, remove it", record)
		}
	}
}

// A descriptor is also the ad-hoc report vocabulary (runAdHocPlan reads
// schemaFields and takes the entity name as its table), so declaring one
// promises that the report path can row-scope that table. Where it cannot,
// auth.ScopeClauseFor errors and the plan fails as a fault rather than
// refusing — which is why an edge type has no descriptor.
func TestEverySchemaDescriptorCanBeRowScopedByTheReportPath(t *testing.T) {
	if len(schemaObjects) == 0 {
		t.Fatal("no schema descriptors — this gate walked nothing, so it proves nothing")
	}
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	rep := ids.NewV7()
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + rep.String(), UserID: rep,
		Permissions: principal.Permissions{RowScope: principal.RowScopeOwn},
	})
	for _, obj := range schemaObjects {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		if obj.Type == datasource.EntityActivity {
			// The timeline scopes through its links, not an owner_id column;
			// runAdHocPlan switches to ActivityScopeClause for exactly this entity.
			if _, err := auth.ActivityScopeClause(ctx, "t", arg); err != nil {
				t.Errorf("activity descriptor cannot be row-scoped: %v", err)
			}
			continue
		}
		if _, err := auth.ScopeClauseFor(ctx, string(obj.Type), "t", arg); err != nil {
			t.Errorf("descriptor %q cannot be row-scoped by the report path (%v) — an ad-hoc plan over "+
				"it would fault instead of refusing", obj.Type, err)
		}
	}
}
