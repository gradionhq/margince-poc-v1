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
//
// The tree the gate reads is itself proven rather than assumed:
// storeEntryPointScope sweeps the whole module for the same entry-point
// shape and reports any that lies outside internal/modules, so a store
// that grows in another tier fails this gate instead of falling out of
// its reach unnoticed.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// ungatedEntryPoints are the ratified auth-free store/service methods.
var ungatedEntryPoints = gatekit.Waive(map[string]string{ // #nosec G101 -- waiver rationales for the fitness gate, not credentials
	// Reached only from worker sweeps, approvals effect executors, or a
	// service that owns the gate above them. Each entry states which.
	"internal/modules/ai:CostReport":                         "aggregates the workspace's OWN ai_call rows under RLS and returns totals, never a record; the cost surface above it takes the grant",
	"internal/modules/ai:DueDeferredBuilds":                  "worker sweep: walks the fleet workspace-by-workspace for builds to re-offer, under the system principal — no human actor exists to gate",
	"internal/modules/ai:RateFor":                            "reads the provider rate card (model pricing), not tenant data — it returns no record and there is no object to grant on",
	"internal/modules/ai:ServedTaskTotals":                   "RLS-scoped aggregate of this workspace's calls for compose/costestimate; returns counts and totals, no record",
	"internal/modules/capture:AgeOutReviewTx":                "age-out sweep write inside the caller's transaction",
	"internal/modules/capture:AwaitingReview":                "review-queue sweep (compose/captureverdictsweeps.go) under the system principal",
	"internal/modules/capture:ClaimDue":                      "worker sweep (compose/captureverdict.go): claims due pending counterparties under the system principal; no request, no human actor",
	"internal/modules/capture:ClaimReviewForAgeOut":          "the claim that serializes the age-out sweep; system principal, no request path",
	"internal/modules/capture:CorrectResolution":             "sweep correction of a verdict it wrote itself, inside the caller's transaction",
	"internal/modules/capture:Defer":                         "sweep bookkeeping for a claimed row — reschedule with backoff; reached only from the same system-principal loop as ClaimDue",
	"internal/modules/capture:ExpireExhausted":               "auto-enrich sweep expiring exhausted budget slots",
	"internal/modules/capture:LinkProposal":                  "sweep bookkeeping: binds the staged proposal to the pending row it was raised from",
	"internal/modules/capture:ListDueOrgs":                   "auto-enrich sweep (compose/captureautoenrich.go) under the system principal",
	"internal/modules/capture:MarkQueued":                    "auto-enrich sweep bookkeeping for an org it just queued",
	"internal/modules/capture:MarkResolved":                  "records the outcome of an auto-applied deep read; effect-executor path, the approval is the authority",
	"internal/modules/capture:NoiseMailForTx":                "sweep read inside the caller's transaction, selecting the loop's own claimed activities",
	"internal/modules/capture:NoiseMailToHide":               "retention sweep read: noise mail eligible for hiding",
	"internal/modules/capture:NoiseMailToRedact":             "retention sweep read: noise mail past its redaction window",
	"internal/modules/capture:PurgeRawCaptureTx":             "retention sweep purge inside the caller's transaction, over activities the same sweep selected",
	"internal/modules/capture:ReconcileDeclined":             "sweep reconciling declined proposals back onto their pending rows",
	"internal/modules/capture:ReleaseBudget":                 "returns the slot ReserveBudget took, same sweep",
	"internal/modules/capture:ReserveBudget":                 "auto-enrich budget reservation for the sweep's own slot; an accounting write with no record and no actor",
	"internal/modules/capture:Resolve":                       "sweep verdict write for a row the loop already claimed; the claim is the authority and there is no principal to gate",
	"internal/modules/capture:ResolveReviewed":               "approvals EFFECT EXECUTOR (compose/captureverdictaccept.go): it runs after a human approved the staged review, and the approval record is the authority — the approvals surface took the grant",
	"internal/modules/capture:Retire":                        "sweep bookkeeping: retires a pending row the loop has finished with, same system-principal path as ClaimDue",
	"internal/modules/capture:RetireExhausted":               "sweep retiring rows that exhausted their attempts",
	"internal/modules/capture:StaleReviews":                  "sweep read: reviews past their window, for the age-out loop",
	"internal/modules/identity:Get":                          "onboarding wizard state, SELF-scoped: onboardingActor resolves the authenticated human and the query is keyed on user_id, so no object grant applies to your own checkpoint",
	"internal/modules/identity:Put":                          "the write half of the same self-scoped wizard state; onboardingActor is the gate and the row is keyed on the acting user",
	"internal/modules/overlay:BlockAutoMap":                  "same: only usermapservice.go calls it, behind requireUserMapAdmin",
	"internal/modules/overlay:Ingest":                        "the sync sweep's mirror write (backfill + refetch jobs) under the worker's system principal; the incumbent connection is the authority, and no human actor exists on this path",
	"internal/modules/overlay:List":                          "row-scoped by the mirror_visibility deny-join rather than auth.Require: resolveActingMirrorUserID + visibilityJoin answer ErrNotFound for an unmapped principal BEFORE the page query runs, and the datasource provider above it takes the object grant",
	"internal/modules/overlay:ListUserMap":                   "reached only through usermapservice.go, whose every entry point takes requireUserMapAdmin (overlay_connection:update + RequireHuman) — the sanctioned Handlers->Service shape, where the service owns the gate and the store beneath it is module-internal",
	"internal/modules/overlay:LoadBackfillCursor":            "sweep checkpoint read, the mirror of SaveBackfillCursor",
	"internal/modules/overlay:LoadReconcileWatermark":        "reconcile-poller checkpoint read",
	"internal/modules/overlay:PurgeRecord":                   "deletion-feed teardown from the reconcile sweep: removes a mirror row the incumbent reports gone, under the system principal",
	"internal/modules/overlay:RecomputeForOwner":             "recomputes mirror_visibility for one incumbent owner after a mapping change; driven by the mapping writes above, which are themselves gated",
	"internal/modules/overlay:RecordSweepFailure":            "the failure half of the same backoff bookkeeping",
	"internal/modules/overlay:RecordSweepSuccess":            "sweep health bookkeeping (backoff state) written by the sweep about itself",
	"internal/modules/overlay:RevalidateEmailMappings":       "sweep revalidation of owner email mappings; no request path reaches it",
	"internal/modules/overlay:SaveBackfillCursor":            "sweep checkpoint write — the backfill's own resume cursor, not a record",
	"internal/modules/overlay:SaveReconcileWatermark":        "reconcile-poller checkpoint write; sweep state, not a record",
	"internal/modules/overlay:SeedUserMap":                   "seeds mirror_user_map at connect time and on the sweep; the connect handler above it takes overlay_connection:update, and the sweep runs as system",
	"internal/modules/overlay:SetManualUserMap":              "same: only usermapservice.go calls it, behind requireUserMapAdmin",
	"internal/modules/overlay:UpsertAssoc":                   "the same sweep's edge write, from backfill",
	"internal/modules/overlay:UpsertUserMap":                 "the per-entry write SeedUserMap and the visibility recompute drive; same two paths, no independent entry",
	"internal/modules/people:ExhaustedDomains":               "triage sweep (compose/capturedomaintriage.go) under the system principal: reads domains whose crawl attempts are spent so the sweep can settle them rather than strand them; no record, no human actor",
	"internal/modules/people:GetMyLinkedInAccount":           "self-only: reads the CALLER's own linkedin_account row, keyed on the authenticated principal's user id, and there is no path here to another member's; an object grant would be the wrong question because a member needs no permission to see their own profile",
	"internal/modules/people:RetireStaleTriageRead":          "triage sweep bookkeeping under the system principal: finishes a dossier that stopped reporting so its domain can be asked again; touches no record and has no human actor",
	"internal/modules/people:ListDueDomains":                 "triage sweep (compose/capturedomaintriage.go) under the system principal: reads domains still owed an organization verdict, no record and no human actor — the twin of capture:ListDueOrgs",
	"internal/modules/people:MarkTriageQueued":               "triage sweep bookkeeping for a domain it just enqueued a crawl for; arms the retry cursor, touches no record",
	"internal/modules/people:MyLinkedInMatchTotals":          "self-only: counts the CALLER's own ghosts, keyed on the authenticated principal's user id, and returns two integers rather than a record; a member needs no permission to be told where their own import stands",
	"internal/modules/people:RenormalizeLinkedInCompanyKeys": "worker maintenance under the system principal: rewrites a DERIVED column (normalized_company) and collapses the duplicates an older normalizer left; no human actor exists to gate, and it returns counts rather than any record",
	"internal/modules/people:SaveMyLinkedInAccount":          "self-only, same row and same key as GetMyLinkedInAccount — a member editing their own LinkedIn profile URL, which no seat including admin may do on their behalf",
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

	// Public-by-design token surfaces: possession of the emailed or
	// published capability is the authority; there is no authenticated
	// principal. What bounds each capability differs — single use, a
	// signature, an expiry, or nothing but its entropy — so each entry
	// names its own, rather than this header claiming one for all of them.
	"internal/modules/activities:ResolveBookingPage":  "public booking page (A16): resolved by slug for the anonymous visitor; writes nothing",
	"internal/modules/consent:ResolvePreferenceToken": "public preference-center resolve: possession of the emailed capability token IS the authority (no session exists). It is NOT signed and NOT single-use — the preference centre is revisitable by design, and one message's link must keep working after the next goes out — so the bounds that stand in for those properties are named here: 256-bit crypto/rand, expiry plus an age ceiling the send path rotates at (0144), and deletion by Art. 17 erasure",
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
	"internal/modules/agents/runner:FailStuckRuns":            "worker sweep under the system principal: closes runs whose resume died mid-loop, which no human requested and none can reach by then. It only moves 'running' to 'failed', so there is no object an actor could be granted or denied",
	"internal/modules/people:BeginSiteRead":                   "worker-loop status transition (queued→running) under the job's workspace context, not a human principal; the human's authority was checked at StartSiteRead and RLS scopes the guarded CAS write",
	"internal/modules/people:DeferSiteRead":                   "worker-loop scheduling transition (running→deferred) under the job's workspace context; the admitted durable job supplies the retry boundary and RLS scopes the guarded CAS write",
	"internal/modules/people:FinishSiteRead":                  "worker-loop status transition (running→terminal) under the job's workspace context, not a human principal; the human's authority was checked at StartSiteRead and RLS scopes the guarded CAS write",
	"internal/modules/people:UpdateSiteReadProgress":          "worker-loop progress hint on a still-running dossier, same seam as Begin/FinishSiteRead: no human principal, StartSiteRead held the gate, RLS scopes the guarded write",
	"internal/modules/people:UpdateSiteReadDraft":             "worker-loop grounded-draft update on a still-running dossier, same seam as progress: admission happened at start and RLS scopes the versioned operational write",
	"internal/modules/approvals:Stage":                        "staging is invoked BY an admitted mutation (the 🟡 path of a gated store call); the staging row records that actor",
	"internal/modules/approvals:StageInTx":                    "transactional form of Stage used by an admitted compose orchestration; it records the same actor and differs only in commit ownership",
	"internal/modules/approvals:StageOrJoinPendingInTx":       "StageInTx's joining twin, admitted the same way and by the same callers; it adds only the join-or-supersede decision over proposals of one kind against one target, never record data",
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
	"internal/modules/search:SeedReembeddingFleet":            "records which workspaces the claimed run must still cover, driven by the reindex DISPATCHER under the system principal; it writes the non-tenant marker and enqueues, never tenant data",
	"internal/modules/search:FinishWorkspaceReembedding":      "the run's own bookkeeping on the non-tenant marker, driven by a reindex workspace job reaching a terminal outcome; no request and no human principal exists in a worker",
	"internal/modules/search:ReleaseReembedding":              "hands the non-tenant marker back at the end of a run, driven by the River jobs themselves, not a human principal",
	"internal/modules/search:PendingByWorkspace":              "fleet rollup read as the system principal (mirrors EmbedGen, embedgen.go:51-56); consumed only by the compose preview/status surface, which is the gated entry point",
	"internal/modules/search:TokenSumByWorkspace":             "fleet rollup read as the system principal, same posture as PendingByWorkspace — an aggregate SUM/COUNT, never row data",
	"internal/modules/search:EntitiesPending":                 "totals PendingByWorkspace across the fleet; same system-principal posture, no row data",
	"internal/modules/search:ReembedWorkspace":                "one reindex workspace job's body, driven under the system principal, same posture as EmbedGen/PendingByWorkspace; the run's own enqueue (via ClaimAndEnqueueReembedding) is the gated entry point",
	"internal/modules/search:SweepWorkspaceEmbeddingDrift":    "periodic worker sweep (ADR-0069 §3a): heals identity-matched embedding gaps under the system principal, same posture as ReembedWorkspace — no request, no human actor to gate",
	"internal/modules/customfields:ActiveColumns":             "called from inside a record store's own gated Get/List/Create/Update, whose object-level RBAC already ran; the column names/types it answers are workspace-visible schema (the same shape custom_field:read already exposes), not row data a second gate would need to narrow",
	"internal/modules/activities:LabeledCaptureCountSince":    "aggregate count read (ADR-0068 cost pre-flight) consumed only by the compose estimator; it returns a single labeled-message COUNT, never row data — RLS scopes it to the workspace and there is nothing for object-RBAC to narrow (same shape as the approvals existence probes)",
	"internal/modules/activities:UnlabeledCaptureEmails":      "classify-backlog read driven by the worker sweep under the workspace GUC, no human principal (ADR-0063); the rows were admitted at capture time and the labels route attention only",
	"internal/modules/activities:SetCaptureLabel":             "classify verdict write driven by the worker sweep under the workspace GUC; a CAS on capture_label IS NULL that touches nothing but the two label columns — attention routing, not a record mutation (§3.2)",
	"internal/modules/activities:LinkCapturedMailTx":          "the `real` disposition's mirror of the hide, driven by the same verdict sweep on the caller's transaction: it attaches the sender's captured mail to the person the verdict just created, so it can only ever link rows the workspace already holds to a record it just authorized — there is no human principal in a sweep for object-RBAC to admit",
	"internal/modules/activities:HideCapturedNoiseTx":         "the ADR-0072 noise disposition's hide, driven by the verdict engine's system principal on the caller's transaction; its authority is the floored verdict that resolved the ledger row, and there is no human principal in a sweep for object-RBAC to admit — the write is idempotent, reversible, and touches only archived_at",
	"internal/modules/activities:RedactCapturedNoiseTx":       "the same disposition's delayed content redaction, driven by the same sweep once the undo window has closed; gating it on a human's permissions would mean a workspace whose reviewer lost access keeps the mail it decided to redact — the obligation outlives any one principal",
	"internal/modules/activities:ReconcileMessageIdentityTx":  "worker-loop correction on the send dispatcher's own transaction, system principal, no human principal in the call: it rewrites the transport identity of a message this workspace ALREADY sent onto the one the provider stamped. It discloses nothing and creates nothing. It reaches exactly two rows, both in the GUC workspace RLS binds every statement to: the send's own activity, which the delivery the caller holds names, and — only when the natural-key index says another row already holds the stamped identity — the provider's captured outbound echo of this same message, which it merges in. WHICH row that second one may be is constrained in SQL rather than trusted from the provider's string: same workspace and source system, an email, outbound, connector-captured, and not created before the send it echoes. A collision matching none of that is refused, not absorbed. Gating any of it on the sender's seat would let a seat revoked between staging and transmit strand a sent message under an identity that exists nowhere on the wire",
	"internal/modules/people:SetChannelIdentityBlocked":       "reachability bookkeeping driven by the telegram ingest worker under the workspace-channel connector principal (compose/telegramingest.go builds that principal onto the context before it classifies the update, so this write has a named actor), never a human caller: the trigger is Telegram's own my_chat_member delivery, which the poller received through a resolved status='connected' connection. It writes no record data — only blocked_at on an identity the same delivery names — RLS scopes the write to the resolved workspace, and the flip is not silent: the audit row and person.updated event it commits alongside are stamped from that same principal, so a reachability change is traceable to the delivery that caused it",
	"internal/modules/people:EnqueueIdentityConflict":         "the sink capture's ensure path hands routeExact's rival pair to (compose/capture.go's raiseIdentityConflict), under the same connector principal with no human in the call: recording that two independently-established keys name two DIFFERENT people writes no key onto either of them, so it can neither merge nor disclose — there is nothing for object-RBAC to admit, and the human authority sits on the queue's own disposition, not on recording that a disagreement exists. Reachability is honestly stated: a conflict needs a candidate carrying two different KINDS of exact key, and no shipped ensure path builds one — Telegram supplies a channel identity and nothing else, and the API create's address lane is refused before the ladder reads (people/creatededupe.go) — so today only the phone and channel lanes can speak, one at a time, and this entry point waits for a second key kind rather than serving traffic",
	"internal/modules/people:SignatureCandidates":             "enrich-backlog read driven by the worker sweep under the workspace GUC, no human principal (ADR-0063 §2.9); reads only connector-created rows still missing both fields",
	"internal/modules/people:MarkSignatureRead":               "the same sweep's read cursor: it records WHICH mail the model was already shown for a person, so the pass stops paying for the same empty signature nightly — a bookkeeping row with no record data, written under the workspace GUC with no human principal to admit",
	"internal/modules/people:OrgNameCandidates":               "promotion-backlog read driven by the nightly sweep under the workspace GUC, no human principal (PO-F-2a); reads only provisionally-named organizations and the signature evidence naming them",
	"internal/modules/people:PromoteOrgName":                  "the sweep's own write, gated by CORROBORATION rather than by a principal: a CAS on name_source='domain' that a human edit or a dossier name structurally beats, and the uncorroborated case never reaches it — it stages a 🟡 proposal whose decision IS the RBAC gate (organization:update)",
	"internal/modules/people:SetOrganizationLifecycleTx":      "the same CAS on the caller's transaction, called by the lifecycle_change accept executor AFTER the approvals service admitted the deciding human against the organization:update grant and the target's row scope",
	"internal/modules/people:PromoteOrgNameTx":                "the same CAS on the caller's transaction, called by the org_name_promotion accept executor AFTER the approvals service admitted the deciding human against the organization:update grant and the target's row scope",
	"internal/modules/people:ApplySignatureFields":            "evidence-gated fill-only-empty write driven by the worker sweep under the workspace GUC (§2.9): NULL-predicate CAS on title, first-phone-only insert, PO-DDL-12 evidence rows — a human's answer is structurally untouchable (GATE-AI-4)",

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
	// ParkTransmitted/RecordFailure/RecordDeferral/MarkInFlight/ClearInFlight
	// are the dispatcher's own state-machine steps, driven by the outbox/River worker
	// under the system principal with no human principal in the call at all;
	// nothing here discloses a record to anyone — the reason each of them
	// writes is an operator-facing transport diagnosis, not tenant data.
	"internal/modules/comms:StageTx":         "derives user_id from the authenticated principal (storekit.Actor) and fails closed with no caller-suppliable override; the activity:create check on the shared transaction admits the send action itself, but the sending IDENTITY is enforced here, in the store, not inherited from that check",
	"internal/modules/comms:StageChannelTx":  "the channel-shaped twin of StageTx and the same posture: it derives user_id from the authenticated principal (stagingUser, shared with StageTx so neither can grow a caller-suppliable override) and runs inside the caller's already-gated activity transaction",
	"internal/modules/comms:Load":            "worker-loop step: the dispatcher claims the next attempt under the system principal (no human principal in a job); admission happened when the message was staged",
	"internal/modules/comms:RecordSent":      "worker-loop terminal transition on the connector's own success receipt, system principal, same posture as Load",
	"internal/modules/comms:Park":            "worker-loop terminal transition on an unretryable provider failure, system principal, same posture as Load",
	"internal/modules/comms:ParkTransmitted": "the same terminal transition for a delivery the provider ALREADY accepted, system principal, same posture as Park: it keeps the provider's own message id on the row when the receipt write failed, so a message the recipient is holding is not recorded as unsent. It reads nothing back and discloses nothing",
	"internal/modules/comms:RecordFailure":   "worker-loop retry-bookkeeping transition on a transient provider failure, system principal, same posture as Load",
	"internal/modules/comms:RecordDeferral":  "worker-loop pacing transition, system principal, same posture as Load: it notes which rule is holding a delivery back and gives back the attempt that dispatch counted, because a deferral reached no provider. It discloses nothing and can only ever leave a pending delivery pending",
	"internal/modules/comms:MarkInFlight":    "worker-loop at-most-once transition, system principal, same posture as Load: it stamps one timestamp on a pending delivery before the provider call so a crashed attempt is visible to the next one. It reads nothing back and discloses nothing",
	"internal/modules/comms:ClearInFlight":   "the retraction half of MarkInFlight, same posture: it nulls that timestamp once the provider gave a definite answer. Both are timestamps about this system's own transport attempt, not tenant data",
})

// storeEntryPointScope proves the gate's single root: every file in the module
// that declares an entry point of this shape lives under internal/modules, or is
// ratified below.
var storeEntryPointScope = gatekit.Scope{
	Roots:   []string{modulesDir},
	Subject: declaresStoreEntryPoint,
	Exempt:  entryPointsOutsideModules,
}

// entryPointsOutsideModules ratifies the files that hold this entry-point shape
// outside internal/modules. Each says what the methods are; none says they are
// correctly gated, because this gate has not judged them — bringing a tier under
// it is its own decision, taken with its own evidence, and ratifying the sweep is
// not that decision. The entries are the ratchet: a file that stops holding the
// shape is reported stale here, so the question cannot be forgotten.
var entryPointsOutsideModules = gatekit.Waive(map[string]string{
	"internal/compose/org360/assemble.go":     "org360.Service.Assemble — a compose read service assembling the record-360 view across domain tables; this package does reference the platform auth gate (auth.Require, EnsureVisible, the scope clauses), but whether the gate's transitive resolution proves that for each entry point is a judgement this change has not made",
	"internal/compose/org360/graph.go":        "org360.Service.Graph — the same read service's relationship graph, in a package that reaches auth through its section helpers; enrolling compose in this gate is a separate decision from proving where the entry points are",
	"internal/compose/org360/dismissal.go":    "org360.Service.DismissSuggestion — the suggestion-dismissal write; it opens with auth.RequireHuman and auth.Require, so it is not a suspected gap, but this gate has not been the thing that checked it",
	"internal/compose/org360/viewbaseline.go": "org360.Service.Acknowledge — the record-view acknowledgement write, likewise opening with auth.RequireHuman and auth.Require; ratified here only as a subject the roots do not cover",
	"internal/compose/orgbrief/service.go":    "orgbrief.Service.Get and .Ask — the organization brief read and its question surface, both opening with auth.RequireHuman; whether RequireHuman alone is the right admission for them is a question for the tier's own review, not for this sweep",
	"internal/compose/runnerservice.go":       "RunnerService.TickWorkspace and .HandleEvent — the agent runner's worker-loop and event-bus seams, which carry no human principal at all; that posture is what the module-side waivers spell out for their sweep entry points, and applying the same reasoning to compose needs the tier brought under the gate first",
	"internal/platform/blobstore/memory.go":   "memoryStore's Put/Get/Delete/Health — a blobstore.Store driver, matched only because the receiver type name ends in \"Store\". It moves opaque bytes under a caller-supplied key and holds no record and no workspace column, so there is no RBAC object for this gate's rule to name",
	"internal/platform/blobstore/s3.go":       "s3Store's Put/Get/Delete/Health — the same driver interface over S3, matched by the same receiver-name suffix; the admission that matters for a blob is taken by the module surface that mints its key, not by an object-storage client",
})

// declaresStoreEntryPoint reports whether the file holds an entry point of the
// shape this gate judges. Integration-tagged files are excluded for the same
// reason the walk excludes them: the obligation binds production stores, and a
// tagged file can never reach a shipped binary.
func declaresStoreEntryPoint(path string, file *ast.File) bool {
	if isIntegrationTagged(path) {
		return false
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && isStoreEntryPoint(fn) {
			return true
		}
	}
	return false
}

// isStoreEntryPoint is the three-part shape: exported, a pointer receiver on a
// *Store or *Service type, and a context.Context parameter.
func isStoreEntryPoint(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || !fn.Name.IsExported() {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	receiver, ok := star.X.(*ast.Ident)
	return ok && storeReceiver(receiver.Name) && takesContext(fn)
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

// collectStoreEntryPoints returns, per package dir, the function index (a
// name shared across receivers merges optimistically — see the package
// comment) plus the list of exported *Store/*Service methods to check.
func collectStoreEntryPoints(t *testing.T) (map[string]map[string]*gateFnInfo, []gateEntry) {
	t.Helper()
	var entries []gateEntry
	for _, src := range storeEntryPointScope.Files(t) {
		dir := filepath.ToSlash(filepath.Dir(src.Path))
		for _, decl := range src.File.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isStoreEntryPoint(fn) {
				continue
			}
			entries = append(entries, gateEntry{dir, fn.Name.Name})
		}
	}
	return packageFunctionIndex(t), entries
}

// packageFunctionIndex indexes every function in every package under the
// scope's roots. It reads WHOLE packages, not only the files that hold an
// entry point, because the auth call that gates a method routinely sits in
// a same-package helper in another file — indexing only the entry-point
// files would report those methods ungated.
func packageFunctionIndex(t *testing.T) map[string]map[string]*gateFnInfo {
	t.Helper()
	pkgs := map[string]map[string]*gateFnInfo{}
	fset := token.NewFileSet()
	for _, root := range storeEntryPointScope.Roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
				isIntegrationTagged(path) {
				return err
			}
			path = filepath.ToSlash(path)
			dir := filepath.ToSlash(filepath.Dir(path))
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
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
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return pkgs
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
	defer ungatedEntryPoints.AssertAllMatched(t)
	defer entryPointsOutsideModules.AssertAllMatched(t)

	pkgs, entries := collectStoreEntryPoints(t)

	for _, e := range entries {
		if reachesAuthGate(pkgs[e.dir], e.name, map[string]bool{}) {
			continue
		}
		key := e.dir + ":" + e.name
		if ungatedEntryPoints.Waived(t, key) {
			continue
		}
		t.Errorf("%s: exported %s reaches no auth gate (directly or via same-package helpers) — every store entry point is RBAC-gated, or the exception is ratified in ungatedEntryPoints", e.dir, e.name)
	}
}

// storeReceiver matches the store and service receivers by SUFFIX, not
// by exact name. A module whose store is called MirrorStore or RunStore
// is no less a store, and matching only the bare names left those
// outside this gate entirely — invisible coverage reads exactly like
// real coverage.
func storeReceiver(name string) bool {
	return strings.HasSuffix(name, "Store") || strings.HasSuffix(name, "Service")
}

// takesContext keeps the gate on ENTRY POINTS. A method that takes no
// context does no request work — option setters (WithClock), accessors
// and constructors — and demanding an auth gate of them would grow a
// ratification list that says nothing about whether the real entry
// points are covered.
func takesContext(fn *ast.FuncDecl) bool {
	for _, param := range fn.Type.Params.List {
		if sel, ok := param.Type.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "context" && sel.Sel.Name == "Context" {
				return true
			}
		}
	}
	return false
}
