// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package events

// The consumer groups: WHO reads the streams, as distinct from which types
// exist and where each one rides, which is the catalog's job.
//
// The two answer different questions and change for different reasons — a new
// event type touches the catalog, a new consuming module touches this file — so
// they are kept apart rather than growing one file that a reader has to scan
// past half of to reach either.

import "sort"

// Group is a §4.3 consumer group: one per consuming module, so each
// module sees every event once and scales horizontally inside the group.
type Group struct {
	Name    string
	Streams []string
}

// Groups returns the consumer groups with their subscribed streams —
// the events.md §4.3 set plus the later consumers; the census test
// mirrors the full list. cg:workflows and cg:read-model subscribe to
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
		// The audience-change corrector: when a human LIMITS a message after
		// the derived models were built, the derived signals citing it narrow
		// and the thread's scan watermark drops so the next extraction pass
		// re-reads under the new audience. Its own group rather than a second
		// handler on cg:graph-edge for the same isolation reason: re-scoping
		// signals must not be able to stall the edge projection, and vice
		// versa.
		{Name: "cg:audience-rescope", Streams: forEntities(activityStreamEntity)},
		// The LinkedIn ghost matcher (ADR-0078 §8b). Its own group rather than
		// a second handler on cg:graph-edge: that consumer lives in the search
		// module and this call belongs to people, and a module never reaches
		// into a sibling. It listens on the person and organization streams —
		// a contact appearing is a chance to attach a ghost, and so is an
		// account appearing, because employer resolution is what most ghosts
		// are waiting on.
		{Name: "cg:linkedin-match", Streams: forEntities(personStreamEntity, organizationStreamEntity)},
		// Turning a won deal into what its partner earned. Its own group
		// because accrual is money: a projection rebuild or an embedding
		// backlog must never be able to stall it, and a failure to accrue must
		// never look like a failure to index. It listens on the deal stream,
		// where deal.stage_changed carries both the win and the reopen that
		// reverses one.
		{Name: "cg:commissions", Streams: forEntities(dealStreamEntity)},
		// The AI-activity projection (ai_task_run): what the rail and the
		// activity feed read. Its own group because a projection backlog must
		// not be able to stall anything that spends money or moves a record,
		// and because it is the only group on the aitask stream — an
		// all-stream group would carry this traffic for nothing.
		{Name: "cg:ai-activity", Streams: forEntities(aiTaskStreamEntity)},
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
