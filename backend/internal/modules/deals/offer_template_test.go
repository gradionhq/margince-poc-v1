// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// TestOfferTemplateUniqueViolation_TranslatesRacingConstraints proves
// the post-write race backstop (offerTemplateUniqueViolation) maps
// each of the two named 23505s to the same typed conflict the pre-check
// path returns, without needing a real concurrent race against
// Postgres: two writers both passing checkTemplateNameConflict /
// checkTemplateDefaultConflict before either INSERT/UPDATE lands is a
// genuine TOCTOU race under READ COMMITTED, but forcing that
// concurrently in a test would need either real goroutines racing
// against a live database (flaky, no fixed outcome) or a sleep-based
// interleaving (forbidden by T11/craft). Since offerTemplateUniqueViolation
// is a pure function of (error, locale), this exercises the exact
// mapping it performs by constructing the pgconn.PgError pgx itself
// hands back on a 23505 — the same shape storekit.UniqueViolation
// unwraps via errors.As. Coverage for the pre-check's clean-path
// equivalent (the common case, no race needed) already lives in the
// integration suite: TestOfferTemplateCreate_DuplicateNameConflict and
// TestOfferTemplateCreate_DefaultConflictRejectedNotAutoDemoted assert
// the same two typed errors surface as the same two named 409s.
func TestOfferTemplateUniqueViolation_TranslatesRacingConstraints(t *testing.T) {
	wrap := func(constraint string) error {
		pgErr := &pgconn.PgError{Code: "23505", ConstraintName: constraint}
		return fmt.Errorf("insert offer_template: %w", pgErr)
	}

	t.Run("name collision", func(t *testing.T) {
		err := offerTemplateUniqueViolation(wrap("offer_template_name_unique"), "de-DE")
		var dup *DuplicateTemplateNameError
		if !errors.As(err, &dup) {
			t.Fatalf("want *DuplicateTemplateNameError, got %T (%v)", err, err)
		}
		if !errors.Is(err, apperrors.ErrConflict) {
			t.Fatal("want errors.Is(err, apperrors.ErrConflict) to hold")
		}
	})

	t.Run("default collision carries the locale", func(t *testing.T) {
		err := offerTemplateUniqueViolation(wrap("uq_offer_template_default"), "en-US")
		var conflict *DefaultConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("want *DefaultConflictError, got %T (%v)", err, err)
		}
		if conflict.Locale != "en-US" {
			t.Fatalf("want locale en-US carried onto the backstop error, got %q", conflict.Locale)
		}
		if !errors.Is(err, apperrors.ErrConflict) {
			t.Fatal("want errors.Is(err, apperrors.ErrConflict) to hold")
		}
	})

	t.Run("unrelated constraint falls through unmapped", func(t *testing.T) {
		if err := offerTemplateUniqueViolation(wrap("some_other_constraint"), "de-DE"); err != nil {
			t.Fatalf("want nil for an unrecognized constraint, got %v", err)
		}
	})

	t.Run("non-violation error falls through unmapped", func(t *testing.T) {
		if err := offerTemplateUniqueViolation(errors.New("boom"), "de-DE"); err != nil {
			t.Fatalf("want nil for a non-23505 error, got %v", err)
		}
	})
}
