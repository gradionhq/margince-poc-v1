// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

// DB-free proofs for candidates.go's two pure functions: projectOccurrence
// (which of the scan window's two candidate years a recurring field's
// month/day belongs to) and validateDateColumn (the pre-SQL refusal a
// caller-given column must clear). Both take only plain values/slices, so
// neither needs the //go:build integration real-Postgres suite
// (candidates_integration_test.go) that exercises the SQL these functions
// sit beside.

import (
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

func TestProjectOccurrence(t *testing.T) {
	t.Run("non-wrap window projects onto to's year", func(t *testing.T) {
		from := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
		to := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC) // fromMMDD <= toMMDD
		got := projectOccurrence(time.August, 1, from, to)
		want := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("projectOccurrence = %v, want %v", got, want)
		}
	})

	t.Run("wrap window, December side projects onto from's year", func(t *testing.T) {
		from := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
		to := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC) // fromMMDD > toMMDD: wraps
		got := projectOccurrence(time.December, 25, from, to)
		want := time.Date(2026, time.December, 25, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("projectOccurrence(Dec 25, wrap window) = %v, want %v (from's year)", got, want)
		}
	})

	t.Run("wrap window, January side projects onto to's year", func(t *testing.T) {
		from := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
		to := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
		got := projectOccurrence(time.January, 10, from, to)
		want := time.Date(2027, time.January, 10, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("projectOccurrence(Jan 10, wrap window) = %v, want %v (to's year)", got, want)
		}
	})

	t.Run("Feb 29 projected into a non-leap year normalizes to Mar 1", func(t *testing.T) {
		from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC) // non-wrap: projects onto to's year, 2026 (not a leap year)
		got := projectOccurrence(time.February, 29, from, to)
		want := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("projectOccurrence(Feb 29, non-leap 2026) = %v, want %v (time.Date's own normalization to Mar 1)", got, want)
		}
	})
}

func TestValidateDateColumn(t *testing.T) {
	cols := []fieldcatalog.Column{
		{Name: "cf_renewal_date", Type: fieldcatalog.TypeDate},
		{Name: "cf_notes", Type: fieldcatalog.TypeText},
	}

	t.Run("unknown column is refused", func(t *testing.T) {
		err := validateDateColumn(cols, "cf_does_not_exist")
		if !errors.Is(err, ErrUnknownDateColumn) {
			t.Errorf("validateDateColumn(unknown) = %v, want ErrUnknownDateColumn", err)
		}
	})

	t.Run("a real but wrong-typed column is refused", func(t *testing.T) {
		err := validateDateColumn(cols, "cf_notes")
		if !errors.Is(err, ErrUnknownDateColumn) {
			t.Errorf("validateDateColumn(wrong type) = %v, want ErrUnknownDateColumn", err)
		}
	})

	t.Run("a real, active date-typed column is accepted", func(t *testing.T) {
		if err := validateDateColumn(cols, "cf_renewal_date"); err != nil {
			t.Errorf("validateDateColumn(cf_renewal_date) = %v, want nil", err)
		}
	})
}
