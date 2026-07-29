// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The store-entry-point admission rule as a fitness function: every
// exported method on a module's *Store or *Service — the seam both the
// HTTP handlers and the MCP tool surface call through — references the
// platform auth gate (object RBAC and/or the row-scope spellings),
// directly or through a same-package helper. A store method without one
// is an ungoverned door into tenant data: reachable by any transport
// wired to it, invisible to review. Row-scope composition itself stays
// a call-site obligation until it moves into the database (the ADR
// direction); this gate pins the half that is statically checkable.
//
// Gatedness is resolved transitively over same-package calls, matched
// by name: a name shared by several functions counts as gated when ANY
// of them references auth — optimistic on purpose, so the gate never
// cries wolf on dispatch it cannot resolve.
//
// Exceptions are explicit, keyed by "package-dir:FuncName", each with
// the rationale that ratified it; a reasonless or stale waiver fails.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// ungatedEntryPoints are the ratified auth-free store/service methods.
var ungatedEntryPoints = map[string]string{ // #nosec G101 -- waiver rationales for the fitness gate, not credentials
	// Authentication IS the gate these methods implement: they run
	// before a principal exists, or mint/retire the session itself.
	"internal/modules/identity:Login":                 "pre-principal: password verification is what admits the actor; there is no principal to gate yet",
	"internal/modules/identity:Logout":                "session retirement; the bearer's possession of the session IS the authority being revoked",
	"internal/modules/identity:Authenticate":          "pre-principal: this resolves the session cookie INTO the principal every other gate consumes",
	"internal/modules/identity:AuthenticateAgent":     "pre-principal: passport verification is what admits the agent actor (every call re-authenticates, ADR-0055)",
	"internal/modules/identity:AuthenticateAgentByID": "pre-principal: the by-id half of passport verification, same admission seam",
	"internal/modules/identity:InstallationWorkspace": "singleton-organization resolution (A107/ADR-0061), bound by the middleware before any principal exists",
	"internal/modules/identity:BootstrapInstallation": "boot-time provisioning under the system principal (A107/ADR-0061); no human principal can exist before it",
	"internal/modules/identity:CreatePasswordReset":   "pre-principal by design (A74): the caller is locked out; enumeration-resistant token mint, authority is control of the mailbox",
	"internal/modules/identity:RedeemPasswordReset":   "pre-principal by design (A74): possession of the single-use emailed token IS the authority being verified",
	"internal/modules/identity:EffectiveRBAC":         "this LOADS the merged role policy the auth gate enforces — gating it on itself would recurse",
	"internal/modules/identity:SeatType":              "seat-tier lookup feeding the auth gate (scope ∧ tier); same layer as EffectiveRBAC, not above it",
	"internal/modules/identity:IssuePassport":         "gated by the explicit Identity parameter (the authenticated session): a passport is minted for that identity only, capped by validScopes",
	"internal/modules/identity:ListPassports":         "gated by the explicit Identity parameter: the query is pinned to on_behalf_of = the caller (admin sees the workspace's)",
	"internal/modules/identity:RevokePassport":        "gated by the explicit Identity parameter: owner-or-admin is checked against the passport's on_behalf_of before revoking",
	"internal/modules/identity:DeactivateUser":        "gated by the explicit Identity parameter: hasRole(admin) refuses before any read or write",
	"internal/modules/identity:ChangeUserRole":        "gated by the explicit Identity parameter: hasRole(admin) refuses before any read or write",
	"internal/modules/identity:InviteUser":            "gated by the explicit Identity parameter: hasRole(admin) refuses before any read or write",
	"internal/modules/identity:ReactivateUser":        "gated by the explicit Identity parameter: hasRole(admin) refuses before any read or write",
	"internal/modules/identity:GetUser":               "roster read (A52): same rationale as ListUsers — a single member read is intentionally visible to every authenticated seat (workspace RLS + authenticated membership is the boundary); \"user\" is deliberately absent from policy.coreObjects",
	"internal/modules/identity:ListUsers":             "roster read (A52): the member roster is intentionally visible to every authenticated seat, by design, not by oversight — a share-subject picker that only some roles could see would be a broken feature, not a narrower one. Workspace RLS + authenticated membership IS the boundary; \"user\" is deliberately absent from policy.coreObjects (the closed RBAC object set), because gating it would mean granting read on it to all five default roles (no role may reasonably be refused the roster) and backfilling every already-seeded workspace's role.permissions — object-level RBAC exists to narrow WHO sees a record among peers, and there is no such narrowing here to express",
	"internal/modules/identity:ListTeams":             "roster read (A52): same rationale as ListUsers — the team list is intentionally visible to every authenticated seat (workspace RLS + authenticated membership is the boundary), and \"team\" is deliberately absent from policy.coreObjects for the same reason: gating it would grant read to every role, not restrict it, while requiring a backfill of every seeded workspace's role.permissions",

	// Public-by-design token surfaces: possession of the single-use
	// token is the authority; there is no authenticated principal.
	"internal/modules/activities:ResolveBookingPage":  "public booking page (A16): resolved by slug for the anonymous visitor; writes nothing",
	"internal/modules/consent:ResolvePreferenceToken": "public preference-center resolve: possession of the emailed capability token IS the authority (no session exists). It is NOT signed and NOT single-use — the preference centre is revisitable by design, and one message's link must keep working after the next goes out — so the bounds that stand in for those properties are named here: 256-bit crypto/rand, expiry plus an age ceiling the send path rotates at (0141), and deletion by Art. 17 erasure",
	"internal/modules/approvals:MintApprovalToken":    "signs the approval JWS for a decision already admitted by Decide; crypto, not admission",
	"internal/modules/approvals:VerifyApprovalToken":  "verifies the approval JWS presented back; the token is the authority being checked",
	"internal/modules/approvals:Redeem":               "redeems a verified approval token: the token (minted for an admitted decision) is the authority",
	"internal/modules/approvals:RedeemInTx":           "transactional form of Redeem: the already-admitted approval token is the authority; the caller supplies only the commit boundary",
	"internal/modules/approvals:RedeemAndApply":       "atomic approval-effect boundary: Redeem performs the authority checks and the callback runs only inside that same transaction",

	// Engine/system seams that never carry a human principal: the
	// worker loop and cross-module effects run as the system actor, and
	// the admission happened at the surface that staged the work.
	"internal/modules/agents/runner:StartRun":                 "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:SaveOutcome":              "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:MarkFailed":               "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:ClaimSuspendedByApproval": "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:EnqueueJob":               "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:ClaimDueJobs":             "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/agents/runner:FinishJob":                "agent-runner persistence driven by the worker loop under the system principal; admission happened at the tool gate that enqueued the run",
	"internal/modules/people:BeginSiteRead":                   "worker-loop status transition (queued→running) under the job's workspace context, not a human principal; the human's authority was checked at StartSiteRead and RLS scopes the guarded CAS write",
	"internal/modules/people:DeferSiteRead":                   "worker-loop scheduling transition (running→deferred) under the job's workspace context; the admitted durable job supplies the retry boundary and RLS scopes the guarded CAS write",
	"internal/modules/people:FinishSiteRead":                  "worker-loop status transition (running→terminal) under the job's workspace context, not a human principal; the human's authority was checked at StartSiteRead and RLS scopes the guarded CAS write",
	"internal/modules/people:UpdateSiteReadProgress":          "worker-loop progress hint on a still-running dossier, same seam as Begin/FinishSiteRead: no human principal, StartSiteRead held the gate, RLS scopes the guarded write",
	"internal/modules/people:UpdateSiteReadDraft":             "worker-loop grounded-draft update on a still-running dossier, same seam as progress: admission happened at start and RLS scopes the versioned operational write",
	"internal/modules/approvals:WithEffect":                   "composition-root wiring (registers the confirm effect); no data access",
	"internal/modules/activities:WithBlobstore":               "composition-root wiring (injects the object store the attachment handlers use); no data access",
	"internal/modules/activities:WithUnsubscribe":             "composition-root wiring (injects the RFC 8058 preference-token linker the send path derives its List-Unsubscribe from); returns a copy of the store, reads and writes nothing",
	"internal/modules/activities:WithPublicBaseURL":           "composition-root wiring (the boot-configured public scheme+host the tokenized unsubscribe link and the minted Message-ID domain are built from); returns a copy of the store, reads and writes nothing",
	"internal/modules/activities:WithMailbox":                 "composition-root wiring (injects the send-grant pre-flight the send path consults); returns a copy of the store, reads and writes nothing",
	"internal/modules/activities:WithDraftOutcome":            "composition-root wiring (injects the recorder that resolves the voice learning signal a served draft opened, inside the send's own transaction); returns a copy of the store, reads and writes nothing",
	"internal/modules/approvals:Stage":                        "staging is invoked BY an admitted mutation (the 🟡 path of a gated store call); the staging row records that actor",
	"internal/modules/approvals:StageInTx":                    "transactional form of Stage used by an admitted compose orchestration; it records the same actor and differs only in commit ownership",
	"internal/modules/approvals:StageUnlessDeclined":          "Stage with one added refusal — it declines to re-offer a proposal a human already rejected — so it is admitted exactly as Stage is, by the gated mutation that reached it; the extra read is of the offers this same proposal produced, never record data",
	"internal/modules/approvals:HasPendingFor":                "existence probe consumed by gated sibling flows (the sweep's duplicate check); returns no record data",
	"internal/modules/approvals:HasPendingKind":               "existence probe consumed by gated sibling flows (the sweep's duplicate check); returns no record data",
	"internal/modules/approvals:RejectedChangesFor":           "reads back the proposals a human already REFUSED, so the gated sibling flow that staged them can tell whether it is about to redo one; the payloads are that flow's own, never record data",
	"internal/modules/approvals:RejectedChangesForTx":         "transactional form of RejectedChangesFor so the refusal check and the caller's write commit as one unit; same read, and the caller supplies only the commit boundary",
	"internal/modules/approvals:WithdrawInTx":                 "retraction of an offer by the module that RAISED it, driven by a sweep with no human principal: the capture ledger ageing out a question nobody answered. It only ever takes a live offer AWAY (forced expiry, the supersession mechanism), so there is no authority to admit — nothing is created, decided, or disclosed, and a decided approval is left alone because what a human answered is not the caller's to take back",
	"internal/modules/deals:SeedDefaultsTx":                   "workspace-provisioning seed invoked by the boot bootstrap under the system principal (the compose-injected edge)",
	"internal/modules/deals:SeedPipelineTx":                   "the configured variant of the same boot seed (A107/ADR-0061): deployment-file pipeline, system principal, compose-injected edge",
	"internal/modules/deals:StageSemantic":                    "vocabulary lookup (stage → open/won/lost) consumed by gated flows; reads config, not records",
	"internal/modules/search:UpsertEmbedding":                 "written by the outbox consumer under the system principal; reads happen through the gated search paths",
	"internal/modules/search:SeedBinding":                     "deployment-metadata marker (embed_store_binding is non-tenant, no workspace_id, no RLS) written once at boot under the system process, same posture as ai/callstore.go's EnsureConfig",
	"internal/modules/search:PopulatedIdentity":               "one-PK read of the non-tenant binding marker; the /readyz seam (Task 17) has no principal to gate on",
	"internal/modules/search:ReindexNeeded":                   "derived signal over the non-tenant marker plus a system-principal entity scan; consumed only by the compose ops surface, which is itself the gated entry point",
	"internal/modules/search:ClaimAndEnqueueReembedding":      "CAS on the non-tenant marker; the compose confirm endpoint (admin+ops write grant, ADR-0068 design §5.6-swap) is the gated entry point that calls it",
	"internal/modules/search:CompleteReembedding":             "CAS on the non-tenant marker driven by the River job's own clean-completion path, not a human principal",
	"internal/modules/search:PendingByWorkspace":              "fleet rollup read as the system principal (mirrors EmbedGen, embedgen.go:51-56); consumed only by the compose preview/status surface, which is the gated entry point",
	"internal/modules/search:TokenSumByWorkspace":             "fleet rollup read as the system principal, same posture as PendingByWorkspace — an aggregate SUM/COUNT, never row data",
	"internal/modules/search:EntitiesPending":                 "totals PendingByWorkspace across the fleet; same system-principal posture, no row data",
	"internal/modules/search:ReembedCorpus":                   "the River job body (Task 15) driven under the system principal, same posture as EmbedGen/PendingByWorkspace; the job's own enqueue (via ClaimAndEnqueueReembedding) is the gated entry point",
	"internal/modules/customfields:ActiveColumns":             "called from inside a record store's own gated Get/List/Create/Update, whose object-level RBAC already ran; the column names/types it answers are workspace-visible schema (the same shape custom_field:read already exposes), not row data a second gate would need to narrow",
	"internal/modules/people:WithFieldCatalog":                "composition-root wiring (injects the fieldcatalog reader the cf_* read/write paths use); no data access",
	"internal/modules/deals:WithFieldCatalog":                 "composition-root wiring (injects the fieldcatalog reader the cf_* read/write paths use); no data access",
	"internal/modules/deals:WithClock":                        "test-only clock injection (mutate-and-return builder like WithFieldCatalog); no data access",
	"internal/modules/activities:LabeledCaptureCountSince":    "aggregate count read (ADR-0068 cost pre-flight) consumed only by the compose estimator; it returns a single labeled-message COUNT, never row data — RLS scopes it to the workspace and there is nothing for object-RBAC to narrow (same shape as the approvals existence probes)",
	"internal/modules/activities:UnlabeledCaptureEmails":      "classify-backlog read driven by the worker sweep under the workspace GUC, no human principal (ADR-0063); the rows were admitted at capture time and the labels route attention only",
	"internal/modules/activities:SetCaptureLabel":             "classify verdict write driven by the worker sweep under the workspace GUC; a CAS on capture_label IS NULL that touches nothing but the two label columns — attention routing, not a record mutation (§3.2)",
	"internal/modules/approvals:EffectKinds":                  "returns the registered staging-kind NAMES this process composed — build-time wiring, not workspace data; it exists so the composition root's fitness test can hold every stageable kind to a decision-grant mapping, and it touches no database at all",
	"internal/modules/activities:LinkCapturedMailTx":          "the `real` disposition's mirror of the hide, driven by the same verdict sweep on the caller's transaction: it attaches the sender's captured mail to the person the verdict just created, so it can only ever link rows the workspace already holds to a record it just authorized — there is no human principal in a sweep for object-RBAC to admit",
	"internal/modules/activities:HideCapturedNoiseTx":         "the ADR-0072 noise disposition's hide, driven by the verdict engine's system principal on the caller's transaction; its authority is the floored verdict that resolved the ledger row, and there is no human principal in a sweep for object-RBAC to admit — the write is idempotent, reversible, and touches only archived_at",
	"internal/modules/activities:RedactCapturedNoiseTx":       "the same disposition's delayed content redaction, driven by the same sweep once the undo window has closed; gating it on a human's permissions would mean a workspace whose reviewer lost access keeps the mail it decided to redact — the obligation outlives any one principal",
	"internal/modules/activities:ReconcileMessageIdentityTx":  "worker-loop correction on the send dispatcher's own transaction, system principal, no human principal in the call: it rewrites the transport identity of a message this workspace ALREADY sent onto the one the provider stamped. It discloses nothing and creates nothing. It reaches exactly two rows, both in the GUC workspace RLS binds every statement to: the send's own activity, which the delivery the caller holds names, and — only when the natural-key index says another row already holds the stamped identity — the provider's captured outbound echo of this same message, which it merges in. WHICH row that second one may be is constrained in SQL rather than trusted from the provider's string: same workspace and source system, an email, outbound, connector-captured, and not created before the send it echoes. A collision matching none of that is refused, not absorbed. Gating any of it on the sender's seat would let a seat revoked between staging and transmit strand a sent message under an identity that exists nowhere on the wire",
	"internal/modules/people:SignatureCandidates":             "enrich-backlog read driven by the worker sweep under the workspace GUC, no human principal (ADR-0063 §2.9); reads only connector-created rows still missing both fields",
	"internal/modules/people:MarkSignatureRead":               "the same sweep's read cursor: it records WHICH mail the model was already shown for a person, so the pass stops paying for the same empty signature nightly — a bookkeeping row with no record data, written under the workspace GUC with no human principal to admit",
	"internal/modules/people:OrgNameCandidates":               "promotion-backlog read driven by the nightly sweep under the workspace GUC, no human principal (PO-F-2a); reads only provisionally-named organizations and the signature evidence naming them",
	"internal/modules/people:PromoteOrgName":                  "the sweep's own write, gated by CORROBORATION rather than by a principal: a CAS on name_source='domain' that a human edit or a dossier name structurally beats, and the uncorroborated case never reaches it — it stages a 🟡 proposal whose decision IS the RBAC gate (organization:update)",
	"internal/modules/people:PromoteOrgNameTx":                "the same CAS on the caller's transaction, called by the org_name_promotion accept executor AFTER the approvals service admitted the deciding human against the organization:update grant and the target's row scope",
	"internal/modules/people:ApplySignatureFields":            "evidence-gated fill-only-empty write driven by the worker sweep under the workspace GUC (§2.9): NULL-predicate CAS on title, first-phone-only insert, PO-DDL-12 evidence rows — a human's answer is structurally untouchable (GATE-AI-4)",
	"internal/modules/overlay:WithBudgetMeter":                "composition-root wiring (injects the shared OVB meter GetOverlayBudget reads); no data access",
	"internal/modules/overlay:WithIncumbentClassesTranslator": "composition-root wiring (injects the canonical->incumbent class mapping SyncStatus's backfill-completeness lookup needs); no data access",
	"internal/modules/overlay:WithIncumbentFactory":           "composition-root wiring (injects the per-connection incumbent adapter builder Connect seeds the owners directory through); no data access — Connect itself remains auth.Require-gated",
	"internal/modules/overlay:WithLogger":                     "composition-root wiring (injects the logger Connect's best-effort seeding reports through); no data access",
	"internal/modules/overlay:WithModeFlipObserver":           "composition-root wiring (injects the dispatcher-cache invalidation Connect/Disconnect notify after commit); no data access — both flip paths remain auth.Require-gated",
	"internal/modules/webhooks:DeliveryEnabled":               "deployment-capability flag (is a signing key configured?): reads no tenant rows, returns a single boolean the gated ListWebhookSubscriptions handler surfaces so the UI can render a not-enabled state — a config posture with nothing for object-RBAC to narrow",

	// comms: delivery machinery, not the message. StageTx runs inside the
	// caller's own transaction, alongside the activity write that already
	// passed the gated activity:create check — the outbound send itself was
	// admitted there. But activity:create alone would only prove the actor
	// may create an activity, not that the delivery may send through THEIR
	// mailbox — the security-relevant fact this store owns — so StageTx
	// itself derives user_id from the authenticated principal on ctx
	// (storekit.Actor) and fails closed when none resolves to an app_user;
	// no caller input can name a different sender. Object-RBAC has nothing
	// left to narrow once that derivation stands. Load/RecordSent/Park/
	// RecordFailure/RecordDeferral are the dispatcher's own state-machine
	// steps, driven by the outbox/River worker under the system principal
	// with no human principal in the call at all; nothing here discloses a
	// record to anyone — the reason each of them writes is an operator-facing
	// transport diagnosis, not tenant data.
	"internal/modules/comms:StageTx":        "derives user_id from the authenticated principal (storekit.Actor) and fails closed with no caller-suppliable override; the activity:create check on the shared transaction admits the send action itself, but the sending IDENTITY is enforced here, in the store, not inherited from that check",
	"internal/modules/comms:Load":           "worker-loop step: the dispatcher claims the next attempt under the system principal (no human principal in a job); admission happened when the message was staged",
	"internal/modules/comms:RecordSent":     "worker-loop terminal transition on the connector's own success receipt, system principal, same posture as Load",
	"internal/modules/comms:Park":           "worker-loop terminal transition on an unretryable provider failure, system principal, same posture as Load",
	"internal/modules/comms:RecordFailure":  "worker-loop retry-bookkeeping transition on a transient provider failure, system principal, same posture as Load",
	"internal/modules/comms:RecordDeferral": "worker-loop pacing transition, system principal, same posture as Load: it notes which rule is holding a delivery back and gives back the attempt that dispatch counted, because a deferral reached no provider. It discloses nothing and can only ever leave a pending delivery pending",
}

// gateFnInfo is what the gate needs to know about one function name in a
// package: whether any body under that name references auth, and every
// name it mentions (the transitive-resolution edges).
type gateFnInfo struct {
	auth  bool
	calls map[string]bool
}

// gateEntry is one exported *Store/*Service method — a store entry point
// the gate must prove reaches auth.
type gateEntry struct{ dir, name string }

// collectStoreEntryPoints parses every non-test, non-integration module
// source file and returns, per package dir, the function index (a name
// shared across receivers merges optimistically — see the package
// comment) plus the list of exported *Store/*Service methods to check.
func collectStoreEntryPoints(t *testing.T) (map[string]map[string]*gateFnInfo, []gateEntry) {
	t.Helper()
	pkgs := map[string]map[string]*gateFnInfo{}
	var entries []gateEntry

	fset := token.NewFileSet()
	err := filepath.WalkDir("internal/modules", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			isIntegrationTagged(path) {
			return err
		}
		path = filepath.ToSlash(path)
		dir := filepath.ToSlash(filepath.Dir(path))
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		if pkgs[dir] == nil {
			pkgs[dir] = map[string]*gateFnInfo{}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			info := pkgs[dir][fn.Name.Name]
			if info == nil {
				info = &gateFnInfo{calls: map[string]bool{}}
				pkgs[dir][fn.Name.Name] = info
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "auth" {
						info.auth = true
					}
					info.calls[sel.Sel.Name] = true
				}
				if id, ok := n.(*ast.Ident); ok {
					info.calls[id.Name] = true
				}
				return true
			})
			if fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			if se, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := se.X.(*ast.Ident); ok && (id.Name == "Store" || id.Name == "Service") {
					entries = append(entries, gateEntry{dir, fn.Name.Name})
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pkgs, entries
}

// reachesAuthGate resolves gatedness transitively over same-package
// calls, matched by name; seen breaks recursion cycles.
func reachesAuthGate(fns map[string]*gateFnInfo, name string, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	info, ok := fns[name]
	if !ok {
		return false
	}
	if info.auth {
		return true
	}
	for c := range info.calls {
		if _, ok := fns[c]; ok && reachesAuthGate(fns, c, seen) {
			return true
		}
	}
	return false
}

func TestEveryStoreEntryPointIsAuthGated(t *testing.T) {
	for fn, rationale := range ungatedEntryPoints {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("ungatedEntryPoints[%s] has no rationale — a waiver must say why no gate is needed", fn)
		}
	}

	pkgs, entries := collectStoreEntryPoints(t)

	used := map[string]bool{}
	for _, e := range entries {
		if reachesAuthGate(pkgs[e.dir], e.name, map[string]bool{}) {
			continue
		}
		key := e.dir + ":" + e.name
		if _, ratified := ungatedEntryPoints[key]; ratified {
			used[key] = true
			continue
		}
		t.Errorf("%s: exported %s reaches no auth gate (directly or via same-package helpers) — every store entry point is RBAC-gated, or the exception is ratified in ungatedEntryPoints", e.dir, e.name)
	}
	for key := range ungatedEntryPoints {
		if !used[key] {
			t.Errorf("ungatedEntryPoints[%s] matches no ungated entry point — stale waiver, remove it", key)
		}
	}
}
