// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The write-shape obligation as a fitness function: every mutation that
// writes an audit row commits a paired outbox event in the same function
// (data-model §11, events.md §4.2 — spelled once in storekit), across
// modules AND the composition layer. A mutation that audits without
// emitting silently exempts itself from the event backbone; this test
// turns that from a reviewer memory into a gate. Exceptions are explicit,
// keyed by package path + function so a same-named function elsewhere is
// never silently waived, and each carries the decision that ratified it —
// an allow-list entry without a reason is a finding, not a pass. A waiver
// that no longer matches an audit-only function is stale and fails too.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// auditOnlyWrites are the ratified audit-without-event functions, keyed
// by "package-dir:FuncName". Every entry carries the rationale for the
// waiver inline, so the gate is self-contained on a clean checkout; an
// entry without a rationale is a finding, not a pass. When the spec's
// events.md gains the missing event types, wiring storekit.Emit into
// these mutations removes the entries.
var auditOnlyWrites = map[string]string{
	"internal/modules/collections:CreateSavedView":  "saved views are per-user view state, not record facts — events.md §5.3c ratifies this config family as audit-only and defines no saved_view.* type",
	"internal/modules/collections:UpdateSavedView":  "saved views are per-user view state, not record facts — events.md §5.3c ratifies this config family as audit-only and defines no saved_view.* type",
	"internal/modules/collections:ArchiveSavedView": "saved views are per-user view state, not record facts — events.md §5.3c ratifies this config family as audit-only and defines no saved_view.* type",
	"internal/modules/collections:CreateList":       "lists are ratified audit-only in V1 — events.md \u00a75.3c defines no list.* types and none is added",
	"internal/modules/collections:ArchiveList":      "lists are ratified audit-only in V1 — events.md \u00a75.3c defines no list.* types and none is added",
	"internal/modules/collections:AddMember":        "lists are ratified audit-only in V1 — events.md \u00a75.3c defines no list.* types and none is added",
	"internal/modules/collections:CreateTag":        "tags are ratified audit-only in V1 — events.md \u00a75.3c defines no tag.* types and none is added",
	"internal/modules/collections:ArchiveTag":       "tags are ratified audit-only in V1 — events.md \u00a75.3c defines no tag.* types and none is added",
	"internal/modules/collections:ApplyTag":         "tags are ratified audit-only in V1 — events.md \u00a75.3c defines no tag.* types and none is added",
	"internal/modules/consent:CreateDSR":            "the closed catalog (events.md \u00a75) defines no dsr.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/consent:UpdateDSR":            "the closed catalog (events.md \u00a75) defines no dsr.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/consent:IssueDoubleOptIn":     "the closed catalog (events.md \u00a75) defines no consent.doi_issued type; the later grant (recordConsent) emits consent.changed \u2014 issuance is attributable via its audit row",
	"internal/modules/agents:Create":                "automation config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no automation.* type; the runs it produces are separately recorded in workflow_run",
	"internal/modules/agents:Update":                "automation config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no automation.* type; the runs it produces are separately recorded in workflow_run",
	"internal/modules/agents:Archive":               "automation config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no automation.* type; the runs it produces are separately recorded in workflow_run",
	"internal/modules/ai:CreateProfile":             "voice DNA is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no voice.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/ai:UpdateProfile":             "voice DNA is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no voice.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/ai:SetDerivedProfile":         "voice DNA is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no voice.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/ai:ArchiveProfile":            "voice DNA is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no voice.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/ai:IngestSource":              "voice DNA is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no voice.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/ai:UpdateSource":              "voice DNA is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no voice.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:CreateProduct":          "the rate-card is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no product.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:UpdateProduct":          "the rate-card is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no product.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:ArchiveProduct":         "the rate-card is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no product.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:UpdateOffer":            "draft-offer edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types (created/sent/accepted/rejected/superseded), no offer.updated",
	"internal/modules/deals:ArchiveOffer":           "offer archive is ratified audit-only \u2014 events.md \u00a75.3 defines no offer.archived type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:AddOfferLineItem":       "draft line edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:UpdateOfferLineItem":    "draft line edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:RemoveOfferLineItem":    "draft line edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/identity:CreateRecordGrant":   "the closed catalog (events.md \u00a75) defines no grant.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/identity:RevokeRecordGrant":   "the closed catalog (events.md \u00a75) defines no grant.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/signals:UpdateSignal":         "human triage (status/severity) is ratified audit-only \u2014 events.md \u00a75.11 defines only signal.detected/signal.resolved (raw\u2192entity attribution, emitted by the resolver), no signal.updated, and the closed-verb law forbids inventing one build-side",
	"internal/modules/signals:ArchiveSignal":        "signal archive is ratified audit-only \u2014 events.md \u00a75.11 defines no signal.archived type and the closed-verb law forbids inventing one build-side",
	"internal/modules/privacy:AssembleSAR":          "the closed catalog (events.md \u00a75) defines no subject-access-export type; the export is a read whose only write IS the audit row",
	"internal/compose/briefs:SnapshotRun":           "the brief read model is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no brief.* type and the closed-verb law forbids inventing one build-side",
	"internal/compose/briefs:markItem":              "the brief read model is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no brief.* type and the closed-verb law forbids inventing one build-side",
	"internal/compose:WriteFiltered":                "filtered export is a read whose only write IS the audit row \u2014 the closed catalog (events.md \u00a75) defines no export type (the same ratification as AssembleSAR)",
}

func TestEveryAuditedMutationEmitsAnEvent(t *testing.T) {
	for fn, rationale := range auditOnlyWrites {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("auditOnlyWrites[%s] has no rationale — a waiver must say why the event is missing", fn)
		}
	}
	used := map[string]bool{}
	fset := token.NewFileSet()
	for _, root := range []string{"internal/modules", "internal/compose"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
				isIntegrationTagged(path) {
				return err
			}
			path = filepath.ToSlash(path)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var audits, emits bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "storekit" {
						switch sel.Sel.Name {
						case "Audit":
							audits = true
						case "Emit":
							emits = true
						}
					}
					return true
				})
				if audits && !emits {
					key := filepath.ToSlash(filepath.Dir(path)) + ":" + fn.Name.Name
					if _, ratified := auditOnlyWrites[key]; ratified {
						used[key] = true
						continue
					}
					t.Errorf("%s: %s calls storekit.Audit without storekit.Emit — every audited mutation ships its event, or the exception is ratified in auditOnlyWrites",
						path, fn.Name.Name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for key := range auditOnlyWrites {
		if !used[key] {
			t.Errorf("auditOnlyWrites[%s] matches no audit-only function — stale waiver, remove it", key)
		}
	}
}
