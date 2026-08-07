// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The fleet enumeration lives at ratified sites only. A `SELECT id FROM
// workspace` inside a module is the signature of the anti-pattern this phase
// removed: one job row looping every tenant, logging each failure and
// returning success, so River records a green sweep over workspaces that
// failed. Dispatchers enumerate; workers take the workspace they were given.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fleetScanMarker is any read of the workspace COLLECTION. It is deliberately
// just `FROM workspace`, not the full `WHERE archived_at IS NULL` predicate:
// idempotency retention enumerates every workspace INCLUDING archived ones, so
// a marker built around the archived-at filter would never have seen the one
// fleet loop that does not use it — the gate would have been green while the
// anti-pattern lived on.
//
// It still only catches the literal spelling. An alias, a newline inside the
// FROM clause, a schema qualification, or a view would evade it. That residual
// is accepted because the gate's job is to stop the shape being re-introduced
// by habit, not by determined circumvention.
const fleetScanMarker = "FROM workspace"

// singleRowPredicates mark a read of ONE workspace — `WHERE id = $1` and its
// siblings. Those are not fleet enumerations and vastly outnumber them, so the
// gate would be useless without this exclusion.
//
// Matched as a PREFIX rather than requiring a bind parameter: the tree also
// resolves one workspace by correlated subquery (`WHERE id = d.workspace_id`)
// and through the GUC (`WHERE id = NULLIF(current_setting(...))`). Any
// predicate equating id to a single value is a single-row read by
// construction — with the exception of a SET-valued RHS, which setPredicates
// takes back. Checked on the marker's own line and the one after it, because
// the repo splits some query literals across lines.
var singleRowPredicates = []string{"WHERE id = ", "WHERE id=", "WHERE w.id = "}

// setPredicates are the RHS spellings that equate id to a SET rather than to
// one value. They look like a single-row predicate to the prefixes above and
// are not one: `WHERE id = ANY($1::uuid[])` reads many workspaces.
var setPredicates = []string{"= ANY", "= any", "IN (", "in ("}

// ratifiedFleetScan is one sanctioned enumeration site. The COUNT is
// load-bearing: several ratified files hold a legitimate scan alongside code
// that must never grow another, and a bare path waiver would wave through a
// re-introduced sweep loop in the same file.
type ratifiedFleetScan struct {
	count  int
	reason string
}

// ratifiedFleetScans are the sanctioned enumeration sites, keyed by
// repo-relative path. A site missing here is a finding; an entry matching no
// remaining site is stale; a count that no longer matches is either a new scan
// to justify or a removed one to un-ratify.
//
// A reason must name which of exactly four things the site is:
//
//  1. a DISPATCHER's own enumeration — it reads the fleet only to enqueue one
//     workspace-scoped job per tenant, and does no tenant work itself;
//  2. a READ — the enumeration and everything it drives write nothing;
//  3. a BOOT path — it runs once at startup, before any job exists to run it;
//  4. TENANT RESOLUTION for an untenanted inbound request — a push or a webhook
//     carries no tenant, so the scan finds the ONE workspace that owns it and
//     the work then happens in that workspace alone.
//
// What a reason may NOT rest on is the site being outside the job layer. Every
// fleet pass is a job now, so "not reachable from a Work method" describes
// nothing durable: the next pass to want this enumeration reaches it from a
// worker, and a waiver granted on that basis would already have waved it
// through.
var ratifiedFleetScans = map[string]ratifiedFleetScan{
	"internal/compose/dispatch.go": {
		2,
		"the dispatch enumerations: the live fleet every pass on behalf of an active tenant fans out over, and the archived-inclusive one BOTH retention passes need — GDPR retention and idempotency claim retention — because archiving a workspace does not un-store the data inside it. Both read the fleet only to enqueue one workspace-scoped job per tenant, and do no tenant work themselves",
	},
	"internal/compose/installation.go": {
		1,
		"bootstrap seed (ADR-0090 §8): reads back the organization row this same transaction just created, to seed the installation settings from it rather than re-deriving identity's own defaulting. Not a fleet read — the transaction holds exactly one workspace by construction, it runs on the boot path with no job and no args to take a workspace from, and ADR-0061 §3 refuses to start when a second exists",
	},
	"internal/modules/identity/installation.go": {
		1,
		"boot path: resolves the singleton organization and refuses to serve when a second exists (ADR-0061 §3) — it IS the workspace authority, not a consumer of it",
	},
	"internal/modules/capture/registry_connections.go": {
		1,
		"collectDue, the due-scan BOTH capture dispatchers drive (gmail_sync via DueConnections, gmail_watch_renew via DueWatches): it enumerates to find due connections it then enqueues one job each for, which is the target shape, not the anti-pattern",
	},
	"internal/modules/capture/channelpoll.go": {
		1,
		"the telegram_poll_sweep DISPATCHER's due-scan — same shape as registry_connections above",
	},
	"internal/modules/capture/push.go": {
		1,
		"tenant resolution: a Pub/Sub push notification names a mailbox and no tenant, so BumpDueByMailbox probes every workspace under its own GUC to find which one holds that mailbox — and bumps only the connections it found there",
	},
	"internal/modules/capture/credentialbackfill.go": {
		1,
		"boot path: one-shot credential migration at startup, before any job runs",
	},
	"internal/modules/ai/voice_build_complete.go": {
		1,
		"the voice_build_retry DISPATCHER's due-scan: enqueues one voice_build per due build, a finer fan-out than per-workspace",
	},
	"internal/modules/overlay/metrics.go": {
		1,
		"a metrics read: aggregates overlay sync lag across the fleet for /metrics exposition, writes nothing",
	},
	"internal/modules/overlay/connectionreads.go": {
		2,
		"DueOverlayConnections is the overlay_reconcile DISPATCHER's due-scan, whose next_sweep_at gate is the sweep backoff — the registry_connections shape; WorkspaceForPortal is tenant resolution, reading which single workspace an incoming webhook portal belongs to",
	},
	"internal/modules/search/binding.go": {
		1,
		"a read: fleetWorkspaceIDs drives pendingStats, the embed-backlog rollup, which visits every live tenant under that tenant's own GUC to count the entities carrying no embedding at the current identity and writes nothing",
	},
}

// countFleetScans returns how many workspace-COLLECTION reads a file makes.
func countFleetScans(src string) int {
	lines := strings.Split(src, "\n")
	n := 0
	for i, line := range lines {
		for offset := 0; ; {
			at := strings.Index(line[offset:], fleetScanMarker)
			if at < 0 {
				break
			}
			end := offset + at + len(fleetScanMarker)
			offset = end
			// A sibling TABLE — workspace_signing_key, workspace_email_domain —
			// is not the workspace collection, so the marker must not run into
			// an identifier character.
			if end < len(line) {
				if c := line[end]; c == '_' || isAlphanumeric(c) {
					continue
				}
			}
			window := line
			if i+1 < len(lines) {
				window += " " + lines[i+1]
			}
			single := false
			for _, pred := range singleRowPredicates {
				if strings.Contains(window, pred) {
					single = true
					break
				}
			}
			for _, pred := range setPredicates {
				if strings.Contains(window, pred) {
					single = false
					break
				}
			}
			if !single {
				n++
			}
		}
	}
	return n
}

func isAlphanumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func TestFleetEnumerationOnlyAtRatifiedSites(t *testing.T) {
	found := map[string]int{}
	paths, err := goFilesUnder("internal")
	if err != nil {
		t.Fatalf("walking internal: %v", err)
	}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if n := countFleetScans(string(src)); n > 0 {
			found[filepath.ToSlash(path)] = n
		}
	}

	for path, n := range found {
		ratified, ok := ratifiedFleetScans[path]
		if !ok {
			t.Errorf("%s enumerates the fleet. A per-workspace pass takes its workspace from job args; only a dispatcher enumerates. If this site really must, ratify it in ratifiedFleetScans with a rationale.", path)
			continue
		}
		if n != ratified.count {
			t.Errorf("%s holds %d fleet scans, ratified for %d. A new one needs its own rationale; a removed one needs the count lowered.", path, n, ratified.count)
		}
	}
	for path, ratified := range ratifiedFleetScans {
		if ratified.reason == "" {
			t.Errorf("%s: waiver without a rationale", path)
		}
		if found[path] == 0 {
			t.Errorf("%s: stale waiver — the site no longer enumerates the fleet; delete it", path)
		}
	}
}
