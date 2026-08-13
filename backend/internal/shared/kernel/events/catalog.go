// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package events

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// StreamPrefix namespaces every CRM stream on the shared gw:events bus
// (03-architecture §3.4: the same bus Dispact rides).
const StreamPrefix = "gw:events:crm:"

const (
	personStreamEntity       = "person"
	organizationStreamEntity = "organization"
	dealStreamEntity         = "deal"
	leadStreamEntity         = "lead"
	activityStreamEntity     = "activity"
	approvalStreamEntity     = "approval"
	captureStreamEntity      = "capture"
	coldstartStreamEntity    = "coldstart"
	auditStreamEntity        = "audit"
	identityStreamEntity     = "identity"
	voiceStreamEntity        = "voice"
)

// streamOverlay is the §5.10 overlay-mirror stream's entity segment — named
// once because the catalog below repeats it across every mirror.* entry.
const streamOverlay = "overlay"

// extensionStreamEntity is the one stream every EXTENSION-authored event rides,
// whichever unit published it.
//
// One stream for the whole tier rather than one per unit, because the stream SET
// is static everywhere it is read — the ops surface enumerates it, the purge
// unlinks it, a consumer group declares its streams at construction — while the
// composed unit set is a property of the build. Per-unit streams would make
// every one of those depend on which units an installation happens to ship.
const extensionStreamEntity = "extension"

// ExtensionEventVersion is the payload schema version every extension event
// carries, and it is 1 forever.
//
// A unit changes the shape of what it publishes by naming a NEW VERB, never by
// bumping this. The alternative is version negotiation across a boundary with no
// shared schema registry: the core cannot validate a unit's payload against
// anything, so a number here would be a promise neither side could keep, and a
// consumer trusting it would be trusting the publisher's own word about a shape
// nobody checked.
const ExtensionEventVersion = 1

// extensionTypeGrammar is the extension event type law, `ext_<namespace>.<verb>`
// — the SQL namespace a unit owns (`ext_` plus its name with hyphens turned to
// underscores), then a lower snake_case verb.
//
// The namespace half keeps one unit's events out of another's name and out of
// the core families entirely, and it is the same token that opens the unit's
// tables and its database role, so a reader who knows one knows the others.
// Nothing here is a grant: the publisher's namespace is derived from the
// INVOCATION at the port, where a unit never spells it, and this grammar only
// says what such a type looks like so the bus can route one.
var extensionTypeGrammar = regexp.MustCompile(`^ext_[a-z0-9_]+\.[a-z][a-z0-9_]*$`)

// IsExtensionType reports whether an event type is extension-authored. It is a
// question about SHAPE, not a registry lookup: the catalog below is the core's
// closed set, while the extension set is whatever the composed units publish —
// which no file in this repository can enumerate.
func IsExtensionType(eventType string) bool {
	return extensionTypeGrammar.MatchString(eventType)
}

// streamEntities are the V1 family streams from events.md, plus the §5.6a
// identity/access-revocation stream, the voice owner-private lifecycle
// stream, and the §5.10 overlay-mirror stream (overlay-mode-only).
// Workspace is a field inside the envelope, never a stream —
// per-tenant streams would explode key count at multi-tenant scale.
var streamEntities = []string{
	personStreamEntity, organizationStreamEntity, dealStreamEntity, leadStreamEntity, activityStreamEntity,
	approvalStreamEntity, captureStreamEntity, coldstartStreamEntity, auditStreamEntity, identityStreamEntity, voiceStreamEntity,
	streamOverlay,
}

// Streams returns the full stream key set, sorted, for the ops surface to
// enumerate — the core families AND the extension tier's one stream. A stream
// left out here is one the data reset does not unlink and no operator can see,
// which is a worse outcome than the entries it would leave behind.
func Streams() []string {
	out := make([]string, 0, len(streamEntities)+1)
	for _, e := range streamEntities {
		out = append(out, StreamPrefix+e)
	}
	out = append(out, StreamPrefix+extensionStreamEntity)
	sort.Strings(out)
	return out
}

// coreStreams is what a CORE consumer group means when it subscribes to
// "everything": the family streams, and deliberately NOT the extension one.
//
// A core consumer has nothing to do with a unit's event and no way to act on
// one. The automation engine would load every live instance and match no
// trigger; the webhook deliverer would query subscriptions for a type the
// public catalog cannot name. Both are pure cost on every extension event, both
// are invisible, and both grow with the tier. A unit's event is delivered to
// the units that ASKED for it, through their own groups.
func coreStreams() []string {
	out := make([]string, len(streamEntities))
	for i, e := range streamEntities {
		out[i] = StreamPrefix + e
	}
	sort.Strings(out)
	return out
}

// ExtensionStream is the stream key every extension-authored event rides. The
// composition builds a unit's consumer group over it by name, and the routing
// test pins that no core group carries it.
func ExtensionStream() string {
	return StreamPrefix + extensionStreamEntity
}

// catalog is the enumerable V1 event catalog (events.md §5.1–§5.10, plus
// the §5.11 signal lifecycle): each type's home stream entity and current
// payload schema version. §5.10 (overlay mirror) is overlay-mode-only —
// these types are only ever emitted for a workspace with x_sor_mode =
// 'overlay' — and the remaining §5.11 type (forecast.period_closed) rides
// E09 — deferred with its work package.
//
// Types whose entity segment is not itself a stream ride their family's
// stream (events.md §1 routing rule): consent.*/retention.* are
// person-lifecycle events, offer.*/pipeline.*/stage.* belong to the
// deal family — each declares its stream home here, and no catalog type
// may imply a stream §4.1 does not define.
var catalog = map[string]struct {
	stream  string
	version int
}{
	"person.created":  {personStreamEntity, 1},
	"person.updated":  {personStreamEntity, 1},
	"person.archived": {personStreamEntity, 1},
	"person.merged":   {personStreamEntity, 1},
	"person.restored": {personStreamEntity, 1},
	"consent.changed": {personStreamEntity, 1},
	// What a contact promised, asked or decided, and a human's correction of
	// it. Both ride the PERSON stream: a subscriber reacting to what somebody
	// said wants the person, and the claim id rides the payload for the reader
	// that needs the row. A correction is published where a moment dismissal is
	// not, because a correction is shared truth and a dismissal is one screen.
	"conversation_claim.captured": {personStreamEntity, 1},
	"conversation_claim.changed":  {personStreamEntity, 1},
	// A member recording or correcting their own LinkedIn authorization. It
	// rides the person stream because the thing it governs is whose network
	// gets read, and consent to read a professional network is the same class
	// of fact as consent.changed beside it.
	"linkedin_account.changed": {personStreamEntity, 1},
	// One import act, not one row: an export is thousands of rows and a
	// per-row event would bury every other event in the stream, while the
	// auditable fact is that a member imported their network at all.
	"linkedin_network.imported": {personStreamEntity, 1},
	// One decision on one connection. It rides the person stream because the
	// decision is ABOUT a contact — and it names neither the contact nor the
	// connection, because a ghost's identity must not travel through the bus.
	"linkedin_match.decided": {personStreamEntity, 1},
	"retention.applied":      {personStreamEntity, 1},

	"organization.created":  {organizationStreamEntity, 1},
	"organization.updated":  {organizationStreamEntity, 1},
	"organization.archived": {organizationStreamEntity, 1},
	"organization.merged":   {organizationStreamEntity, 1},

	"deal.created":       {dealStreamEntity, 1},
	"pipeline.created":   {dealStreamEntity, 1},
	"pipeline.updated":   {dealStreamEntity, 1},
	"pipeline.archived":  {dealStreamEntity, 1},
	"stage.created":      {dealStreamEntity, 1},
	"stage.updated":      {dealStreamEntity, 1},
	"stage.archived":     {dealStreamEntity, 1},
	"deal.updated":       {dealStreamEntity, 1},
	"deal.stage_changed": {dealStreamEntity, 1},
	"deal.owner_changed": {dealStreamEntity, 1},
	"deal.archived":      {dealStreamEntity, 1},
	"deal.restored":      {dealStreamEntity, 1},
	// The project rides the deal family stream: it is the body of work the
	// deals hang off, and a consumer interested in one is interested in both.
	"project.created":       {dealStreamEntity, 1},
	"project.updated":       {dealStreamEntity, 1},
	"project.phase_changed": {dealStreamEntity, 1},
	"project.archived":      {dealStreamEntity, 1},
	"offer.created":         {dealStreamEntity, 1},
	"offer.sent":            {dealStreamEntity, 1},
	"offer.accepted":        {dealStreamEntity, 1},
	"offer.rejected":        {dealStreamEntity, 1},
	"offer.superseded":      {dealStreamEntity, 1},

	"lead.created":      {leadStreamEntity, 1},
	"lead.updated":      {leadStreamEntity, 1},
	"lead.promoted":     {leadStreamEntity, 1},
	"lead.disqualified": {leadStreamEntity, 1},

	"activity.captured": {activityStreamEntity, 1},
	"activity.updated":  {activityStreamEntity, 1},
	"activity.archived": {activityStreamEntity, 1},
	// §5.11: a thread-matched inbound is an activity-family fact, emitted
	// by capture alongside activity.captured (EVT-SEM-14 — idempotent per
	// reply; a duplicate inbound for the same reply does not re-emit).
	"engagement.reply": {activityStreamEntity, 1},

	"approval.requested": {approvalStreamEntity, 1},
	"approval.decided":   {approvalStreamEntity, 1},

	"capture.received":   {captureStreamEntity, 1},
	"capture.normalized": {captureStreamEntity, 1},
	"capture.failed":     {captureStreamEntity, 1},
	"capture.skipped":    {captureStreamEntity, 1},

	// §5.11: signal is not one of the nine stream entities — the
	// detection lifecycle rides the capture stream (events.md §5.11
	// stream-routing rule).
	"signal.detected": {captureStreamEntity, 1},
	"signal.resolved": {captureStreamEntity, 1},

	"coldstart.read_back_proposed": {coldstartStreamEntity, 1},
	"coldstart.accepted":           {coldstartStreamEntity, 1},
	"coldstart.rejected":           {coldstartStreamEntity, 1},

	"audit.appended": {auditStreamEntity, 1},

	// §5.6a: the access-revocation cascade (B-EP03.10) — user, role and
	// passport are identity-owned facts, so all three ride the identity
	// stream rather than gaining per-entity streams of their own.
	"user.invited":              {identityStreamEntity, 1},
	"user.password_link_issued": {identityStreamEntity, 1},
	"user.deactivated":          {identityStreamEntity, 1},
	"user.reactivated":          {identityStreamEntity, 1},
	"role.changed":              {identityStreamEntity, 1},
	"passport.revoked":          {identityStreamEntity, 1},
	"onboarding.state_changed":  {identityStreamEntity, 1},

	"voice.profile_created":        {voiceStreamEntity, 1},
	"voice.profile_updated":        {voiceStreamEntity, 1},
	"voice.profile_archived":       {voiceStreamEntity, 1},
	"voice.corpus_changed":         {voiceStreamEntity, 1},
	"voice.build_changed":          {voiceStreamEntity, 1},
	"voice.version_changed":        {voiceStreamEntity, 1},
	"voice.draft_outcome_recorded": {voiceStreamEntity, 1},

	// §5.10: the overlay mirror's own stream — emitted only in overlay
	// mode. mirror.write_rejected is reserved for the branch-2 write
	// path but registered now so the catalog is complete.
	"mirror.conflict":        {streamOverlay, 1},
	"mirror.budget_degraded": {streamOverlay, 1},
	"mirror.write_rejected":  {streamOverlay, 1},
	"mirror.deleted":         {streamOverlay, 1},

	// §4.3: the incumbent connection lifecycle — a genuine SoR mutation
	// (unlike mirror ingest), so it carries the full write shape and
	// rides the same overlay-mode-only stream as the mirror it gates.
	"incumbent.connected":    {streamOverlay, 1},
	"incumbent.disconnected": {streamOverlay, 1},
}

// pipelineEventTypes are the capture-pipeline events that may ride the bus
// WITHOUT a subject entity ref. A pipeline step can be subject-less by
// nature — capture.skipped names NOTHING (an excluded personal message
// creates no row), yet the spec still requires it on the bus as the
// machine-checkable "personal mail is never ingested" proof (capture.md
// AC1.3, EVT-SEM-10). These events carry no entity handle, but they DO keep
// the ledger trace link (audit_log OR system_log) so the outcome stays
// attributable — Validate enforces the trace, only the entity is relaxed.
var pipelineEventTypes = map[string]struct{}{
	"capture.received":   {},
	"capture.normalized": {},
	"capture.failed":     {},
	"capture.skipped":    {},
}

// IsPipelineEvent reports whether an event type is an entity-less
// pipeline-event class member (see pipelineEventTypes): its envelope may
// carry an empty Entity ref where a normal event must name its subject.
func IsPipelineEvent(eventType string) bool {
	_, ok := pipelineEventTypes[eventType]
	return ok
}

// Types returns every catalog event type, sorted — the enumerable set
// codegen and the naming fitness test walk.
func Types() []string {
	out := make([]string, 0, len(catalog))
	for t := range catalog {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// StreamFor routes an event type to its stream key. An unknown type is a
// programming error the publisher must surface before the outbox write —
// an unroutable row would wedge the relay forever.
//
// The catalog is consulted FIRST and the extension grammar second, so a core
// type can never be routed by shape. That ordering is belt to the braces of
// TestNoCatalogTypeLooksLikeAnExtensionType, which holds the two vocabularies
// apart at their source.
func StreamFor(eventType string) (string, error) {
	if spec, ok := catalog[eventType]; ok {
		return StreamPrefix + spec.stream, nil
	}
	if IsExtensionType(eventType) {
		return ExtensionStream(), nil
	}
	return "", fmt.Errorf("events: %q is neither an events.md §5 catalog type nor an ext_<namespace>.<verb> extension type", eventType)
}

// VersionOf returns the current payload schema version of a catalog type
// (0 for an unknown type; Validate rejects those via StreamFor first), and
// ExtensionEventVersion for an extension type. Publishers stamp envelopes
// from here — never a literal — so a future v2 bump happens in exactly one
// place, and so the extension port has no version of its own to get wrong.
func VersionOf(eventType string) int {
	if spec, ok := catalog[eventType]; ok {
		return spec.version
	}
	if IsExtensionType(eventType) {
		return ExtensionEventVersion
	}
	return 0
}

// Group is a §4.3 consumer group: one per consuming module, so each
// module sees every event once and scales horizontally inside the group.
type Group struct {
	Name    string
	Streams []string
}

// Groups returns the seven V1 consumer groups with their subscribed
// streams (events.md §4.3). cg:workflows and cg:read-model subscribe to
// everything by design; cg:audit-stream also does, because its "all
// actor.type=agent events" slice cuts across every stream and Redis
// consumer groups can only partition by stream, not by envelope field —
// the actor filter is in-process, like the workspace filter.
func Groups() []Group {
	all := coreStreams()
	forEntities := func(entities ...string) []string {
		keys := make([]string, len(entities))
		for i, e := range entities {
			keys[i] = StreamPrefix + e
		}
		sort.Strings(keys)
		return keys
	}
	return []Group{
		{Name: "cg:context-graph", Streams: forEntities(personStreamEntity, organizationStreamEntity, dealStreamEntity, activityStreamEntity, leadStreamEntity)},
		// The interaction-edge projection (CG-DDL-1 / ADR-0078). Its OWN group
		// rather than a second handler on cg:context-graph: a projection
		// rebuild must not be able to stall embedding freshness, and the two
		// have unrelated failure modes.
		{Name: "cg:graph-edge", Streams: forEntities(activityStreamEntity, personStreamEntity)},
		// The LinkedIn ghost matcher (ADR-0078 §8b). Its own group rather than
		// a second handler on cg:graph-edge: that consumer lives in the search
		// module and this call belongs to people, and a module never reaches
		// into a sibling. It listens on the person and organization streams —
		// a contact appearing is a chance to attach a ghost, and so is an
		// account appearing, because employer resolution is what most ghosts
		// are waiting on.
		{Name: "cg:linkedin-match", Streams: forEntities(personStreamEntity, organizationStreamEntity)},
		// Filling a contact from what their employer's site already published
		// (ADR-0072 arc). Its own group for the same reason as the matcher
		// above: the deep read fills a published person DURING the crawl, so
		// a contact who arrives afterwards is never matched against what that
		// site said. It listens on the person stream alone — the fill is
		// keyed on the contact, and an account appearing enriches nobody
		// until a person is filed against it, which is itself a person event.
		{Name: "cg:person-auto-enrich", Streams: forEntities(personStreamEntity)},
		// Automatic enrichment from a LICENSED provider (ADR-0101/PI-EVT-1).
		// Its own group rather than a second handler on the pass above,
		// because the two differ in what a failure costs: that one reads a
		// page the workspace already crawled, this one SPENDS the customer's
		// credits, and a consumer whose retries buy data must not share a
		// cursor with one whose retries are free.
		{Name: "cg:person-data", Streams: forEntities(personStreamEntity)},
		{Name: "cg:overnight-agent", Streams: forEntities(activityStreamEntity, dealStreamEntity, leadStreamEntity, approvalStreamEntity)},
		{Name: "cg:workflows", Streams: all},
		{Name: "cg:capture", Streams: forEntities(captureStreamEntity)},
		{Name: "cg:flow-bridge", Streams: forEntities(personStreamEntity, dealStreamEntity, activityStreamEntity)},
		{Name: "cg:read-model", Streams: all},
		{Name: "cg:audit-stream", Streams: all},
		// The outbound-webhook fan-out (E10/S-E10.6): a subscription may
		// name any published event type, so this group listens on every
		// stream and matches per-subscription event_types in-process.
		{Name: "cg:webhooks", Streams: all},
	}
}

// SplitType breaks a catalog type into its <entity>.<verb> segments
// (events.md §1). Multi-word verbs keep their underscores
// ("stage_changed", "read_back_proposed").
func SplitType(eventType string) (entity, verb string, err error) {
	entity, verb, ok := strings.Cut(eventType, ".")
	if !ok || entity == "" || verb == "" {
		return "", "", fmt.Errorf("events: %q is not <entity>.<verb>", eventType)
	}
	return entity, verb, nil
}
