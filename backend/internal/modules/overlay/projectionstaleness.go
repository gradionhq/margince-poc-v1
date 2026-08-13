// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// A mirror row holds a PROJECTION, and it is re-projected only when the
// incumbent's own baseline advances (mirrorstore.go's staleness guard). A
// mapping change therefore leaves already-mirrored rows holding a payload the
// current declaration would never produce, indefinitely — and the flip freezes
// the mirror and writes those payloads as durable native rows, so whatever a
// row holds at flip time becomes permanent. This file owns the one comparison
// that detects it, shared by the flip preflight (flipstate.go) and the
// operator-facing sync status (syncstatus.go) so the two can never disagree
// about which rows are out of date.

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
	// Encoded here rather than handed to pgx as a map, so the argument's wire
	// shape is this package's decision and not an inference from how Postgres
	// happens to resolve the parameter's type behind the cast.
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
