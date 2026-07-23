// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The write-shape obligation as a fitness function: every mutation that
// writes an audit row commits a paired outbox event on the same static call path
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
	"os"
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
	"internal/modules/collections:CreateSavedView":       "saved views are per-user view state, not record facts — events.md §5.3c ratifies this config family as audit-only and defines no saved_view.* type",
	"internal/modules/collections:UpdateSavedView":       "saved views are per-user view state, not record facts — events.md §5.3c ratifies this config family as audit-only and defines no saved_view.* type",
	"internal/modules/collections:ArchiveSavedView":      "saved views are per-user view state, not record facts — events.md §5.3c ratifies this config family as audit-only and defines no saved_view.* type",
	"internal/modules/collections:CreateList":            "lists are ratified audit-only in V1 — events.md \u00a75.3c defines no list.* types and none is added",
	"internal/modules/collections:ArchiveList":           "lists are ratified audit-only in V1 — events.md \u00a75.3c defines no list.* types and none is added",
	"internal/modules/collections:AddMember":             "lists are ratified audit-only in V1 — events.md \u00a75.3c defines no list.* types and none is added",
	"internal/modules/collections:CreateTag":             "tags are ratified audit-only in V1 — events.md \u00a75.3c defines no tag.* types and none is added",
	"internal/modules/collections:ArchiveTag":            "tags are ratified audit-only in V1 — events.md \u00a75.3c defines no tag.* types and none is added",
	"internal/modules/collections:ApplyTag":              "tags are ratified audit-only in V1 — events.md \u00a75.3c defines no tag.* types and none is added",
	"internal/modules/consent:CreateDSR":                 "the closed catalog (events.md \u00a75) defines no dsr.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/consent:UpdateDSR":                 "the closed catalog (events.md \u00a75) defines no dsr.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/consent:finalizeErasureFulfil":     "the audit-only finalize step of FulfilErasure: the closed catalog (events.md \u00a75) defines no dsr.* type; the closed-verb law forbids inventing one build-side (the erase side effect emits its own person.* event inside privacy.ErasePerson)",
	"internal/modules/consent:IssueDoubleOptIn":          "the closed catalog (events.md \u00a75) defines no consent.doi_issued type; the later grant (recordConsent) emits consent.changed \u2014 issuance is attributable via its audit row",
	"internal/modules/automation:Create":                 "automation config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no automation.* type; the runs it produces are separately recorded in workflow_run",
	"internal/modules/automation:Update":                 "automation config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no automation.* type; the runs it produces are separately recorded in workflow_run",
	"internal/modules/automation:Archive":                "automation config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no automation.* type; the runs it produces are separately recorded in workflow_run",
	"internal/modules/webhooks:CreateSubscription":       "webhook-subscription config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no webhook_subscription.* type; the deliveries it produces are separately recorded in webhook_delivery",
	"internal/modules/webhooks:UpdateSubscription":       "webhook-subscription config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no webhook_subscription.* type; the deliveries it produces are separately recorded in webhook_delivery",
	"internal/modules/webhooks:RotateSecret":             "webhook-subscription config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no webhook_subscription.* type; the rotation is attributable via its audit row",
	"internal/modules/webhooks:ArchiveSubscription":      "webhook-subscription config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no webhook_subscription.* type; archive stops delivery and is attributable via its audit row",
	"internal/modules/webhooks:requireReplay":            "a human-initiated delivery replay is ratified audit-only \u2014 it re-attempts an existing webhook_delivery; the closed catalog (events.md \u00a75) defines no webhook.* type",
	"internal/modules/deals:CreateProduct":               "the rate-card is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no product.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:UpdateProduct":               "the rate-card is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no product.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:ArchiveProduct":              "the rate-card is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no product.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:UpdateOffer":                 "draft-offer edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types (created/sent/accepted/rejected/superseded), no offer.updated",
	"internal/modules/deals:ArchiveOffer":                "offer archive is ratified audit-only \u2014 events.md \u00a75.3 defines no offer.archived type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:SetPdfAssetRef":              "persisting the rendered PDF's blob ref is ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.rendered, and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:AddOfferLineItem":            "draft line edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:UpdateOfferLineItem":         "draft line edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:RemoveOfferLineItem":         "draft line edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:AcceptOfferLineItem":         "accepting a staged draft line (E03.21a) is a draft line edit \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:AddStagedOfferLines":         "staging AI-drafted draft lines (E03.21a) is a draft line edit \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated; a staged line is invisible until AcceptOfferLineItem, which carries the same ratified waiver",
	"internal/modules/deals:CreateOfferTemplate":         "offer templates are workspace-authored config, the Product/Quota precedent \u2014 the closed catalog (events.md \u00a75) defines no offer_template.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:UpdateOfferTemplate":         "offer templates are workspace-authored config, the Product/Quota precedent \u2014 the closed catalog (events.md \u00a75) defines no offer_template.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:ArchiveOfferTemplate":        "offer templates are workspace-authored config, the Product/Quota precedent \u2014 the closed catalog (events.md \u00a75) defines no offer_template.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/people:createOrJoinSiteRead":       "the deep-read dossier is operational crawl status, not a record fact \u2014 the closed catalog (events.md \u00a75) defines no site_read.* type; the facts a read produces land through its staged proposals, each emitting its own event on accept",
	"internal/modules/people:setDedupeDisposition":       "the queue verdict is review-flow state, not a record fact \u2014 the closed catalog (events.md \u00a75) defines no dedupe.* type; the merge arm's person.merged/organization.merged carries the bus-visible outcome",
	"internal/modules/people:reopenDedupeCandidate":      "the queue verdict is review-flow state, not a record fact \u2014 the closed catalog (events.md \u00a75) defines no dedupe.* type; the merge arm's person.merged/organization.merged carries the bus-visible outcome",
	"internal/modules/identity:CreateRecordGrant":        "the closed catalog (events.md \u00a75) defines no grant.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/identity:RevokeRecordGrant":        "the closed catalog (events.md \u00a75) defines no grant.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/signals:UpdateSignal":              "human triage (status/severity) is ratified audit-only \u2014 events.md \u00a75.11 defines only signal.detected/signal.resolved (raw\u2192entity attribution, emitted by the resolver), no signal.updated, and the closed-verb law forbids inventing one build-side",
	"internal/modules/signals:ArchiveSignal":             "signal archive is ratified audit-only \u2014 events.md \u00a75.11 defines no signal.archived type and the closed-verb law forbids inventing one build-side",
	"internal/modules/activities:UploadAttachment":       "attachments are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no attachment.* type and a polymorphic attachment has no single stream to ride; the closed-verb law forbids inventing one build-side",
	"internal/modules/activities:ArchiveAttachment":      "attachments are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no attachment.* type and a polymorphic attachment has no single stream to ride; the closed-verb law forbids inventing one build-side",
	"internal/modules/activities:MarkScanResult":         "attachments are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no attachment.* type and a polymorphic attachment has no single stream to ride; the closed-verb law forbids inventing one build-side",
	"internal/modules/customfields:createInTx":           "the closed catalog (events.md \u00a75) defines no custom_field.* type \u2014 the spec's custom-fields.md \u00a7Events ratifies the audit entry as the add/rename/retire trail, and a cross-object catalog change has no single family stream to ride (the attachments precedent)",
	"internal/modules/customfields:Rename":               "the closed catalog (events.md \u00a75) defines no custom_field.* type \u2014 the spec's custom-fields.md \u00a7Events ratifies the audit entry as the add/rename/retire trail",
	"internal/modules/customfields:Retire":               "the closed catalog (events.md \u00a75) defines no custom_field.* type \u2014 the spec's custom-fields.md \u00a7Events ratifies the audit entry as the add/rename/retire trail",
	"internal/modules/customfields:setOptionsInTx":       "the closed catalog (events.md \u00a75) defines no custom_field.* type \u2014 the spec's custom-fields.md \u00a7Events ratifies the audit entry as the add/rename/retire trail",
	"internal/modules/quotas:CreateQuota":                "quota targets are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no quota.* type (E09's forecast.period_closed is a period-close fact, deferred with its work package) and the closed-verb law forbids inventing one build-side",
	"internal/modules/quotas:UpdateQuota":                "quota targets are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no quota.* type (E09's forecast.period_closed is a period-close fact, deferred with its work package) and the closed-verb law forbids inventing one build-side",
	"internal/modules/quotas:ArchiveQuota":               "quota targets are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no quota.* type (E09's forecast.period_closed is a period-close fact, deferred with its work package) and the closed-verb law forbids inventing one build-side",
	"internal/modules/privacy:AssembleSAR":               "the closed catalog (events.md \u00a75) defines no subject-access-export type; the export is a read whose only write IS the audit row",
	"internal/modules/privacy:tombstoneCollateralScrubs": "the erasure's single retention.applied on the person is the bus-visible fact for the whole scrub; these tombstones exist to bound each collateral record's field-history projection, and the closed catalog (events.md \u00a75) defines no per-collateral erasure type to ride",
	"internal/compose/briefs:SnapshotRun":                "the brief read model is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no brief.* type and the closed-verb law forbids inventing one build-side",
	"internal/compose/briefs:markItem":                   "the brief read model is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no brief.* type and the closed-verb law forbids inventing one build-side",
	"internal/compose/briefs:resurfaceExpiredSnoozes":    "the brief read model is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no brief.* type and the closed-verb law forbids inventing one build-side",
	// WriteFiltered no longer belongs here: the bulk export is a non-entity
	// operational event, so it moved from storekit.Audit to storekit.LogSystem
	// (system_log, 0074) \u2014 it writes no audit_log row at all now.
}

func TestEveryAuditedMutationEmitsAnEvent(t *testing.T) {
	for fn, rationale := range auditOnlyWrites {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("auditOnlyWrites[%s] has no rationale — a waiver must say why the event is missing", fn)
		}
	}
	used := map[string]bool{}
	emissionPathsByDir := map[string]map[string]bool{}
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
			dir := filepath.Dir(path)
			emissionPaths, ok := emissionPathsByDir[dir]
			if !ok {
				emissionPaths, err = emissionBearingFunctions(fset, dir)
				if err != nil {
					return err
				}
				emissionPathsByDir[dir] = emissionPaths
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var audits, emits bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.SelectorExpr:
						if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == "storekit" {
							switch node.Sel.Name {
							case "Audit", "AuditWithEvidence":
								audits = true
							case "Emit", "EmitEvent", "EmitEventForEntity":
								emits = true
							}
						}
					case *ast.CallExpr:
						if callee, ok := node.Fun.(*ast.Ident); ok && emissionPaths[callee.Name] {
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

// emissionBearingFunctions follows package-local plain-function calls so the
// gate accepts one named event-envelope helper without forcing every caller to
// spell Emit. Method helpers deliberately do not qualify: a receiver abstraction
// would make the transactional write shape too hard to verify statically here.
func emissionBearingFunctions(fset *token.FileSet, dir string) (map[string]bool, error) {
	emits := map[string]bool{}
	calls := map[string][]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || isIntegrationTagged(path) {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SelectorExpr:
					if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == "storekit" {
						switch node.Sel.Name {
						case "Emit", "EmitEvent", "EmitEventForEntity":
							emits[fn.Name.Name] = true
						}
					}
				case *ast.CallExpr:
					if callee, ok := node.Fun.(*ast.Ident); ok {
						calls[fn.Name.Name] = append(calls[fn.Name.Name], callee.Name)
					}
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for caller, callees := range calls {
			if emits[caller] {
				continue
			}
			for _, callee := range callees {
				if emits[callee] {
					emits[caller] = true
					changed = true
					break
				}
			}
		}
	}
	return emits, nil
}
