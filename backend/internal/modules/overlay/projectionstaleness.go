// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// A mirror row holds a PROJECTION, and it is re-projected only when the
// incumbent's own baseline advances (mirrorstore.go's staleness guard). A
// mapping change therefore leaves already-mirrored rows holding a payload the
// current declaration would never produce, indefinitely — and the flip freezes
// the mirror and writes those payloads as durable native rows, so whatever a
// row holds at flip time becomes permanent. This file owns the one comparison
// that detects it, shared by the flip preflight (flipstate.go), the
// operator-facing sync status (syncstatus.go), and the sweep phase that
// converges those rows (StaleProjections, below) so the three can never
// disagree about which rows are out of date — the flip must block on exactly
// the rows the sweep re-fetches, or it blocks on rows nothing will ever clear.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// WithProjectionFingerprints wires the current declaration fingerprints
// (e.g. hubspot.ProjectionFingerprints) the flip preflight and the sync status
// compare every mirror row against — keyed by INCUMBENT class, resolved to the
// mirror's canonical object_class through WithIncumbentClassesTranslator. See
// the Service doc for why this package cannot hold the registry itself.
// Returns s so compose can chain it onto NewService's result.
func (s *Service) WithProjectionFingerprints(byIncumbentClass map[string]string) *Service {
	s.projectionFingerprints = byIncumbentClass
	return s
}

// staleProjectionSQL is the predicate both aggregations apply to one
// overlay_mirror row: its class is one this deployment can currently judge,
// and no current declaration for that class produced the payload it holds. It
// reads $3 — the jsonb currentProjectionFingerprints returns — so every query
// embedding it must bind that object at position 3.
//
// The outer `?` tests KEY existence on the object, which is what excludes a
// class the map does not name: a retired mapping can never match a current
// fingerprint, so counting its rows as stale would block the flip with a
// staleness nothing could ever clear. The inner `?` tests ELEMENT existence in
// that class's array, since several declarations can project onto one
// canonical class. coalesce makes a row that records NO declaration — NULL,
// mirrored before the column existed — compare as stale rather than as
// unknown: nothing verified that payload against the current mapping either.
const staleProjectionSQL = `($3::jsonb ? object_class
	    AND NOT ($3::jsonb -> object_class ? coalesce(projection_fingerprint, '')))`

// currentProjectionFingerprints builds the staleProjectionSQL argument for the
// classes the mirror actually holds: canonical object_class → the fingerprints
// a current declaration could have stamped on it, as the jsonb object literal
// the predicate reads. A class it omits is one this deployment cannot judge,
// and is excluded from staleness entirely rather than guessed at.
func (s *Service) currentProjectionFingerprints(mirroredClasses []string) (string, error) {
	byClass := make(map[string][]string, len(mirroredClasses))
	for _, class := range mirroredClasses {
		if fingerprints := s.fingerprintsFor(class); len(fingerprints) > 0 {
			byClass[class] = fingerprints
		}
	}
	return encodeFingerprintSets(byClass)
}

// encodeFingerprintSets renders the staleProjectionSQL argument — canonical
// object class → the fingerprints a current declaration could have stamped on
// its rows — as the jsonb object literal the predicate reads. Encoded here
// rather than handed to pgx as a map, so the argument's wire shape is this
// package's decision and not an inference from how Postgres happens to resolve
// the parameter's type behind the cast.
func encodeFingerprintSets(byClass map[string][]string) (string, error) {
	encoded, err := json.Marshal(byClass)
	if err != nil {
		return "", fmt.Errorf("overlay: encoding the current projection fingerprints: %w", err)
	}
	return string(encoded), nil
}

// fingerprintsFor answers the fingerprints a CURRENT declaration could have
// stamped on a row of canonicalClass, or nil when this deployment cannot say.
// Three ways it cannot: neither collaborator is wired (a role composed without
// them judges nothing rather than declaring every row stale), the class has no
// declared mapping at all (a retired one — its rows must not block a flip
// forever), and the partial case below.
func (s *Service) fingerprintsFor(canonicalClass string) []string {
	if s.toIncumbentClasses == nil || len(s.projectionFingerprints) == 0 {
		return nil
	}
	incumbentClasses, ok := s.toIncumbentClasses(canonicalClass)
	if !ok {
		return nil
	}
	fingerprints := make([]string, 0, len(incumbentClasses))
	for _, incumbentClass := range incumbentClasses {
		if fingerprint := s.projectionFingerprints[incumbentClass]; fingerprint != "" {
			fingerprints = append(fingerprints, fingerprint)
		}
	}
	// A class only PARTLY fingerprinted cannot be judged either: one of the
	// declarations that project onto it is unaccounted for, and a row this
	// comparison would call stale might be exactly what that one produces.
	if len(fingerprints) != len(incumbentClasses) {
		return nil
	}
	return fingerprints
}

// staleProjectionIDsSQL names the rows ONE declaration must re-project: the
// rows it governs (the containment filter, $2 — see governedRowsFilter) whose
// stored payload it did not produce (staleProjectionSQL over $3, holding that
// declaration's own current fingerprint). Ordered by external id so a bounded
// pass takes a stable prefix rather than a different arbitrary slice each time,
// and a row already re-projected simply drops out of the set.
//
// A row with an un-drained local write ($4) is left out: ingest's
// no-clobber-dirty guard would refuse the re-projection, so re-fetching it
// would spend an incumbent read on a write that cannot land. Such a row blocks
// the flip on its own pending state anyway, and it re-enters this set as soon
// as the write drains.
const staleProjectionIDsSQL = `
SELECT external_id FROM overlay_mirror
WHERE object_class = $1
  AND fields @> $2::jsonb
  AND sync_state <> $4
  AND ` + staleProjectionSQL + `
ORDER BY external_id
LIMIT $5`

// StaleProjections answers up to limit external ids of the mirror rows m
// governs whose payload m's CURRENT declaration did not produce — the set the
// reconcile sweep re-fetches so they converge on today's mapping. It embeds
// the same predicate the flip preflight and the sync status count with, so the
// rows the sweep clears are exactly the rows the flip blocks on.
//
// The ids are the INCUMBENT's own (external_id), which is what a re-fetch
// names, and the caller re-reads them under m.Source.
func (s *MirrorStore) StaleProjections(ctx context.Context, m ObjectMapping, limit int) ([]string, error) {
	governed, err := governedRowsFilter(m)
	if err != nil {
		return nil, err
	}
	// m's own fingerprint alone, not every declaration projecting onto
	// m.Target: the containment filter has already narrowed the rows to the
	// ones m produced, so a sibling declaration's fingerprint cannot appear
	// among them — and admitting one would spare a row nothing re-projects.
	current, err := encodeFingerprintSets(map[string][]string{m.Target: {Fingerprint(m)}})
	if err != nil {
		return nil, err
	}
	var externalIDs []string
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, staleProjectionIDsSQL, m.Target, governed, current, syncStatePendingSync, limit)
		if err != nil {
			return fmt.Errorf("overlay: listing the %s rows an older declaration projected: %w", m.Source, err)
		}
		externalIDs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("overlay: collecting the %s rows an older declaration projected: %w", m.Source, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return externalIDs, nil
}

// governedRowsFilter renders the jsonb containment filter that picks out the
// mirror rows m's declaration produced, as opposed to a sibling declaration's.
// The mirror is keyed by the CANONICAL class, and several declarations can
// project onto one — the five engagement classes all land on "activity" —
// while a re-fetch names the INCUMBENT class, so a row read back under the
// wrong sibling would be a live incumbent read for a record that class does
// not hold. Const is what separates them: its entries are copied verbatim into
// every payload the declaration projects (mapping.go's Apply), and each
// registry's own TestEveryDeclarationSharingATargetIsSeparableByItsConstants
// holds it to declaring constants that contradict between siblings. A
// declaration with no constants therefore has its target to itself, and the
// empty object — which every payload contains — is the honest filter for it.
func governedRowsFilter(m ObjectMapping) (string, error) {
	if len(m.Const) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(m.Const)
	if err != nil {
		return "", fmt.Errorf("overlay: encoding the %s declaration's constant fields: %w", m.Source, err)
	}
	return string(encoded), nil
}

// mirroredClasses lists the canonical object classes overlay_mirror currently
// holds — the classes both the backfill-convergence check and the projection
// comparison are asked about, read once so a preflight never asks the same
// question twice inside its own transaction.
func mirroredClasses(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT object_class FROM overlay_mirror ORDER BY object_class`)
	if err != nil {
		return nil, fmt.Errorf("overlay: listing the mirrored classes: %w", err)
	}
	classes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("overlay: collecting the mirrored classes: %w", err)
	}
	return classes, nil
}
